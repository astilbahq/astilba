package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrPollLeaseHeld distinguishes temporary poll contention from command conflicts.
// HTTP clients still receive the ordinary conflict response.
var ErrPollLeaseHeld = fmt.Errorf("poll lease held: %w", domain.Conflict)

// pollLeaseWait bounds taking or releasing a poll's lease, once its request no longer can.
const pollLeaseWait = 5 * time.Second

// A database lease bounds overlapping polls across management replicas.
func (s *Service) Poll(ctx context.Context, a Identity) (*api.ExecutionCommand, error) {
	return s.poll(ctx, a, nil)
}

// PollAgent also returns terminal sessions waiting for this agent. They wake the poll
// but are not journaled commands, and they cannot be replayed once their offer closes.
func (s *Service) PollAgent(ctx context.Context, a Identity) (api.AgentPoll, error) {
	var out api.AgentPoll
	command, err := s.poll(ctx, a, &out)
	out.Command = command
	return out, err
}

func (s *Service) poll(ctx context.Context, a Identity, out *api.AgentPoll) (*api.ExecutionCommand, error) {
	id := uuid.New()
	// A client that goes away must not decide either step. Cancelling a statement as it
	// commits reports a failure for a lease that was taken, and that lease would then turn
	// away the restarted agent's polls, and its commands and sessions, until it expired.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), pollLeaseWait)
		defer cancel()
		_, _ = s.Store.Pool.Exec(cleanup, "UPDATE servers SET poll_id=NULL,poll_until=NULL WHERE id=$1 AND poll_id=$2", a.ID, id)
	}()
	claim, claimed := context.WithTimeout(context.WithoutCancel(ctx), pollLeaseWait)
	n, err := s.Store.Pool.Exec(claim, `UPDATE servers SET poll_id=$2,poll_until=now()+interval '30 seconds' WHERE id=$1 AND (poll_until IS NULL OR poll_until<now())`, a.ID, id)
	claimed()
	if err != nil {
		return nil, err
	}
	if n.RowsAffected() != 1 {
		return nil, ErrPollLeaseHeld
	}
	deadline := time.NewTimer(PollDuration)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var verified uuid.UUID
	for {
		// Check revocation/expiry after each wait, not only at request entry.
		if _, err = s.Authenticate(ctx, a.ID, a.Token); err != nil {
			return nil, err
		}
		if out != nil {
			offers, err := s.troubleshootingOffers(ctx, a)
			if err != nil {
				return nil, err
			}
			if len(offers) > 0 {
				out.TroubleshootingOffers = &offers
			}
		}
		var raw []byte
		var cancel bool
		var state string
		err = s.Store.Pool.QueryRow(ctx, `SELECT command,cancel_requested,state FROM execution_operations WHERE server_id=$1 AND NOT(kind='crosshost' AND parent_id IS NULL) AND state IN ('dispatched','running','needs_attention') AND NOT EXISTS(SELECT 1 FROM cross_host_recoveries r WHERE r.parent_id=execution_operations.parent_id AND NOT r.opened AND COALESCE(command->'crosshost'->>'phase','')<>'recovery-open') AND (parent_id IS NULL OR command->'recoveryCheckpoint' IS NULL OR command->'crosshost'->>'phase' IN ('checkpoint','checkpoint-closed') OR EXISTS(SELECT 1 FROM cross_host_checkpoints cp WHERE cp.parent_id=execution_operations.parent_id AND cp.id::text=command->'recoveryCheckpoint'->>'checkpointId' AND cp.acknowledged) OR EXISTS(SELECT 1 FROM execution_operations root JOIN servers primary_server ON primary_server.id=root.server_id WHERE root.id=execution_operations.parent_id AND primary_server.revoked)) ORDER BY COALESCE(command->'crosshost'->>'phase'='checkpoint',false) DESC, (kind='network' AND COALESCE(command->'network'->>'action','')='restrict') DESC,created_at LIMIT 1`, a.ID).Scan(&raw, &cancel, &state)
		if err == nil {
			var c api.ExecutionCommand
			if err = json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			if (a.Role == "build" && c.Kind != "update" && c.Kind != "pipeline") || (a.Role == "deployment" && c.Kind == "pipeline") {
				return nil, domain.Forbidden
			}
			if s.Store.Hosted {
				tx, e := s.Store.Pool.Begin(ctx)
				if e != nil {
					return nil, e
				}
				e = s.commandAdmission(ctx, tx, a.Workspace, c)
				_ = tx.Rollback(ctx)
				if e != nil {
					if !errors.Is(e, store.SubscriptionRequired) {
						return nil, e
					}
					cancel = true
				}
			}
			if !cancel {
				verify := state == "dispatched" && verified != c.Id
				allowed, e := s.commandAllowed(ctx, a.Workspace, c, verify)
				if e != nil {
					return nil, e
				}
				if !allowed {
					cancel = true
				}
				// The provider call can take many seconds, and a command's secrets leave soon
				// after it, so everything read before it is read again: the agent's credential,
				// the operation and what the call was checked against.
				if verify && allowed {
					verified = c.Id
					continue
				}
			}
			c.CancelRequested = cancel
			// Existing agents compare hydrated visual payloads byte-for-byte.
			// Other command kinds have always accepted credential-free cancellation.
			// Hydration finishes a pipeline no runner holds, and adds no credentials to one it might.
			if cancel && c.Kind != "pipeline" && (c.Kind != "visual" || a.CredentialFreeVisualCancel) {
				return &c, nil
			}
			if c.Kind == "upgrade" {
				if err = s.hydrateUpgrade(ctx, &c); err != nil {
					return nil, err
				}
			}
			if c.Kind == "backup" || c.Kind == "schedule" {
				if err = s.hydrateBackup(ctx, &c); err != nil {
					return nil, err
				}
			}
			if c.Kind == "pipeline" {
				var deliver bool
				if deliver, err = s.hydratePipeline(ctx, a, &c); err != nil {
					return nil, err
				}
				if !deliver {
					continue
				}
			}
			if c.Kind == "crosshost" {
				if err = s.hydrateCrossHost(ctx, &c); err != nil {
					return nil, err
				}
			}
			if c.Kind == "visual" {
				if err = s.hydrateVisual(ctx, &c); err != nil {
					return nil, err
				}
			}
			return &c, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if out != nil && out.TroubleshootingOffers != nil {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, nil
		case <-tick.C:
		}
	}
}

func (s *Service) commandAllowed(ctx context.Context, ws uuid.UUID, c api.ExecutionCommand, verify bool) (bool, error) {
	var allowed bool
	err := s.withGit(ctx, ws, s.legacyRunKeys(ws), func(git *gitAnswers) error {
		tx, err := s.Store.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		ask := git
		if !verify {
			ask = nil
		}
		if allowed, err = s.previewCommandAllowed(ctx, tx, ask, ws, c); err != nil || !allowed {
			return err
		}
		allowed, err = s.maintenanceEffectAllowed(ctx, tx, ws, c)
		return err
	})
	return allowed, err
}

// boundedNote keeps an operation's note to what the API promises and PostgreSQL can store:
// host output and Compose errors can hold any bytes, and invalid UTF-8 grows when encoded.
func boundedNote(note string) string {
	note = note[:min(len(note), 1024)]
	return strings.ReplaceAll(strings.ToValidUTF8(note, ""), "\x00", " ")
}

func (s *Service) Report(ctx context.Context, a Identity, r api.AgentReport) error {
	if r.Sequence < 1 || r.Sequence >= portability.MaxCounter || len(r.Steps) > 16 {
		return domain.Invalid("report")
	}
	r.Note = boundedNote(r.Note)
	if r.State != "running" && r.State != "needs_attention" && !Terminal(r.State) {
		return domain.Invalid("state")
	}
	// A provider call between passes must not count against the agent's report.
	received := time.Now()
	return s.withGit(ctx, a.Workspace, s.legacyRunKeys(a.Workspace), func(git *gitAnswers) error { return s.applyReport(ctx, git, received, a, r) })
}

// applyReport takes its report by value because a pass rewrites it, and a pass that asks a
// provider a question is rolled back and made again from what the agent sent.
func (s *Service) applyReport(ctx context.Context, git *gitAnswers, received time.Time, a Identity, r api.AgentReport) error {
	var tx pgx.Tx
	var err error
	// A network command's report can write network members, which revocation and the sweep write
	// under the workspace lock, so it takes the workspace's write turn whether or not it carries a
	// receipt. Taking servers before network members here and the reverse there would deadlock.
	network := r.Network != nil
	if !network {
		if network, err = s.Store.Queries.ReportsOnNetworkCommand(ctx, db.ReportsOnNetworkCommandParams{ID: r.Id, ServerID: a.ID}); err != nil {
			return err
		}
	}
	if network {
		var done func()
		if tx, done, err = s.Store.BeginWorkspaceWrite(ctx, a.Workspace); err != nil {
			return err
		}
		defer done()
	} else {
		if tx, err = s.Store.Pool.Begin(ctx); err != nil {
			return err
		}
		defer tx.Rollback(ctx)
	}
	// Lock identity before operation, matching operator admission. Revocation cannot race acceptance.
	valid, err := lockAgentCredential(ctx, tx, a)
	if err != nil {
		return err
	}
	if !valid {
		return domain.Unauthenticated
	}
	var raw []byte
	var seq int64
	var state api.ExecutionState
	err = tx.QueryRow(ctx, `SELECT command,report_sequence,state FROM execution_operations WHERE id=$1 AND server_id=$2 FOR UPDATE`, r.Id, a.ID).Scan(&raw, &seq, &state)
	if err != nil {
		return err
	}
	var c api.ExecutionCommand
	if err = json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if c.Generation != r.Generation || c.PlanHash != r.PlanHash || (a.Role == "build" && c.Kind != "update" && c.Kind != "pipeline") || (a.Role == "deployment" && c.Kind == "pipeline") {
		return domain.Forbidden
	}
	// Poll never delivers a coordination root. Only the controller may settle one: a
	// terminal report would stop coordinate resuming and releasing the other hosts.
	if c.Kind == "crosshost" && c.ParentId == nil {
		return domain.Forbidden
	}
	if r.Sequence <= seq {
		return tx.Commit(ctx)
	}
	if Terminal(state) {
		return domain.Conflict
	}
	if c.Kind != "pipeline" && r.Pipeline != nil {
		return domain.Invalid("pipeline")
	}
	if c.Kind == "pipeline" {
		if err = s.pipelineReport(ctx, tx, git, received, c, &r); err != nil {
			return err
		}
	}
	if c.Kind != "upgrade" && r.Upgrade != nil {
		return domain.Invalid("upgrade")
	}
	if c.Kind == "upgrade" {
		if err = s.upgradeReport(ctx, tx, c, &r); err != nil {
			return err
		}
	}
	if c.Kind != "visual" && c.Kind != "upgrade" && r.Observation != nil {
		return domain.Invalid("observation")
	}
	if c.Kind == "visual" {
		if err = s.visualReport(ctx, tx, c, &r); err != nil {
			return err
		}
	}
	if err = s.crossHostReport(ctx, tx, a.Workspace, c, &r); err != nil {
		return err
	}
	if err = s.networkReport(ctx, tx, a.Workspace, c, &r, true); err != nil {
		return err
	}
	steps, _ := stablejson.Marshal(r.Steps)
	_, err = tx.Exec(ctx, `UPDATE execution_operations SET state=$2,report_sequence=$3,steps=$4,note=$5 WHERE id=$1`, r.Id, r.State, r.Sequence, steps, r.Note)
	if err != nil {
		return err
	}
	if Terminal(r.State) {
		if err = s.enqueuePreviewForEnvironment(ctx, tx, a.Workspace, c.EnvironmentId); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
