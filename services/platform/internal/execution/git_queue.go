package execution

import (
	"context"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// gitQueue runs the work that Git webhooks start: refreshes, previews and GitHub App fan-out.
// A workspace can sign as many deliveries as it likes, so this work has workers of its own, and
// a burst of it never delays operation dispatch, DNS or the sweeps in the default queue.
//
// Open-source River claims a queue's jobs by priority and then oldest first, with no share per
// workspace, so jobs here take two measures to share it. At insert, a workspace's jobs past its
// first gitQueueWorkers waiting go to gitBacklogPriority, behind every other workspace's waiting
// jobs however many it queues. At start, workspaceTurn lets a workspace use all but one worker,
// and has a workspace that already runs a job step aside while a workspace with none running has
// one waiting. A workspace with nothing running therefore starts its job on the worker kept free,
// or behind at most a few others' jobs. This is fairness, not exclusion: the workspace lock still
// orders the work itself.
const (
	gitQueue                 = "platform_git"
	gitQueueWorkers          = 4
	gitPriority        int16 = 1
	gitBacklogPriority int16 = 4
	// workspaceTurnWait is short, since a job that steps aside only waits for a worker to free.
	workspaceTurnWait = time.Second
	// workspaceTurnStale outlasts the longest timeout in the queue, a preview's minute, so a job
	// still marked running after it belongs to a client that stopped and holds no worker.
	workspaceTurnStale = 70 * time.Second
)

// queuedStates are a job's states before it runs.
var queuedStates = []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateScheduled}

type workspaceMetadata struct {
	Workspace uuid.UUID `json:"workspace"`
	// Event marks a preview job that a signed request event asked for, which previewEventLimit counts.
	Event bool `json:"event,omitempty"`
}

// insertGitJob files a job under its workspace, for workspaceTurn to find, at the priority its
// workspace's waiting jobs leave it. With once set, a job with the same kind and arguments that
// is available to run at that priority or sooner is the same work, so none is added. One that is
// running may have read what this job is meant to see, one waiting out a retry delay would hold
// it back, and one further back in the queue would make it wait as a backlog, so none of those
// counts.
func (s *Service) insertGitJob(ctx context.Context, tx pgx.Tx, filed workspaceMetadata, args river.JobArgs, opts river.InsertOpts, once bool) error {
	byWorkspace, err := stablejson.Marshal(workspaceMetadata{Workspace: filed.Workspace})
	if err != nil {
		return err
	}
	waiting, err := s.Queue.JobListTx(ctx, tx, river.NewJobListParams().Queues(gitQueue).States(queuedStates...).Metadata(string(byWorkspace)).First(gitQueueWorkers))
	if err != nil {
		return err
	}
	opts.Priority = int(gitPriority)
	if len(waiting.Jobs) >= gitQueueWorkers {
		opts.Priority = int(gitBacklogPriority)
	}
	if once {
		raw, err := stablejson.Marshal(args)
		if err != nil {
			return err
		}
		priorities := []int16{gitPriority}
		if opts.Priority == int(gitBacklogPriority) {
			priorities = append(priorities, gitBacklogPriority)
		}
		same, err := s.Queue.JobListTx(ctx, tx, river.NewJobListParams().Queues(gitQueue).Kinds(args.Kind()).States(rivertype.JobStateAvailable).
			Priorities(priorities...).Where("args = @args::jsonb", river.NamedArgs{"args": string(raw)}).First(1))
		if err != nil || len(same.Jobs) > 0 {
			return err
		}
	}
	if opts.Metadata, err = stablejson.Marshal(filed); err != nil {
		return err
	}
	_, err = s.Queue.InsertTx(ctx, tx, args, &opts)
	return err
}

// workspaceTurn snoozes job when its workspace already runs gitQueueWorkers-1 jobs that started
// before it, or runs any while a workspace with none running has a job waiting at normal
// priority. A job filed under no workspace always runs, and while waiting counts as one with none
// running.
func (s *Service) workspaceTurn(ctx context.Context, job *rivertype.JobRow, ws uuid.UUID) error {
	if job.AttemptedAt == nil {
		return nil
	}
	filed, err := stablejson.Marshal(workspaceMetadata{Workspace: ws})
	if err != nil {
		return err
	}
	running, err := s.Queue.JobList(ctx, river.NewJobListParams().
		Queues(gitQueue).
		States(rivertype.JobStateRunning).
		Metadata(string(filed)).
		Where("attempted_at > @stale AND (attempted_at,id) < (@attempted,@id)", river.NamedArgs{
			"stale":     job.AttemptedAt.Add(-workspaceTurnStale),
			"attempted": *job.AttemptedAt,
			"id":        job.ID,
		}).
		First(gitQueueWorkers-1))
	if err != nil || len(running.Jobs) == 0 {
		return err
	}
	if len(running.Jobs) >= gitQueueWorkers-1 {
		return river.JobSnooze(workspaceTurnWait)
	}
	// Only normal priority counts: a backlog waits behind everything anyway. The list reads
	// river_job unaliased, which the predicate names to compare each waiting job's workspace
	// with the running ones.
	waiting, err := s.Queue.JobList(ctx, river.NewJobListParams().
		Queues(gitQueue).
		States(rivertype.JobStateAvailable).
		Priorities(gitPriority).
		Where(`scheduled_at <= @attempted AND NOT metadata @> @filed::jsonb AND NOT EXISTS(
			SELECT 1 FROM river_job r WHERE r.queue = river_job.queue AND r.state = 'running'
				AND r.metadata->'workspace' = river_job.metadata->'workspace' AND r.attempted_at > @stale)`, river.NamedArgs{
			"attempted": *job.AttemptedAt,
			"filed":     string(filed),
			"stale":     job.AttemptedAt.Add(-workspaceTurnStale),
		}).
		First(1))
	if err != nil {
		return err
	}
	if len(waiting.Jobs) > 0 {
		return river.JobSnooze(workspaceTurnWait)
	}
	return nil
}
