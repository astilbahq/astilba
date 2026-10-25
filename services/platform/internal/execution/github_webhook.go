package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"net/http"
	"strings"
)

type githubEvent struct {
	App          int64  `json:"app"`
	Kind         string `json:"kind"`
	Installation int64  `json:"installation"`
	Repository   int64  `json:"repository"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
}
type githubFanoutArgs struct {
	Delivery string `json:"delivery"`
}

func (githubFanoutArgs) Kind() string { return "platform_github_app_fanout_v1" }

type githubReconcileArgs struct {
	Workspace  uuid.UUID `json:"workspace"`
	Connection uuid.UUID `json:"connection"`
	Generation int64     `json:"generation"`
}

func (githubReconcileArgs) Kind() string { return "platform_github_app_reconcile_v1" }
func (s *Service) ReceiveGitHub(ctx context.Context, headers http.Header, body []byte) error {
	if s.GitHub == nil || s.GitHub.VerifyWebhook(headers, body) != nil {
		return domain.Forbidden
	}
	delivery := headers.Get("X-GitHub-Delivery")
	if delivery == "" || len(delivery) > 200 || strings.ContainsAny(delivery, "\x00\r\n") {
		return domain.Invalid("delivery")
	}
	kind := headers.Get("X-GitHub-Event")
	if kind != "push" && kind != "installation" && kind != "installation_repositories" {
		return nil
	}
	var payload struct {
		Installation struct {
			ID    int64 `json:"id"`
			AppID int64 `json:"app_id"`
		} `json:"installation"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return domain.Invalid("event")
	}
	event := githubEvent{App: s.GitHub.Config.ID, Kind: kind, Installation: payload.Installation.ID, Repository: payload.Repository.ID}
	switch kind {
	case "push":
		if event.Installation < 1 || event.Repository < 1 {
			return domain.Invalid("installation")
		}
		push, e := pipeline.VerifyPush("github", s.GitHub.Config.WebhookSecret, event.Repository, headers, body)
		if e != nil {
			return domain.Invalid("push")
		}
		event.Branch = push.Branch
		event.Commit = push.Commit
	default:
		if event.Installation < 1 || payload.Installation.AppID != s.GitHub.Config.ID {
			return domain.Invalid("installation")
		}
	}
	raw, _ := json.Marshal(event)
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Serialize global retention and delivery/body conflict checks.
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(714646)"); e != nil {
		return e
	}
	var previous string
	e = tx.QueryRow(ctx, "SELECT body_hash FROM github_app_deliveries WHERE delivery_id=$1", delivery).Scan(&previous)
	if e == nil {
		if previous != pipeline.Hash(string(body)) {
			return domain.Conflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM github_app_deliveries WHERE completed AND delivery_id IN (SELECT delivery_id FROM github_app_deliveries WHERE completed ORDER BY received_at DESC OFFSET 9000)"); e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM github_app_deliveries").Scan(&count); e != nil {
		return e
	}
	if count >= 10000 {
		return domain.RateLimited
	}
	if _, e = tx.Exec(ctx, "INSERT INTO github_app_deliveries(delivery_id,body_hash,event) VALUES($1,$2,$3)", delivery, pipeline.Hash(string(body)), raw); e != nil {
		return e
	}
	if _, e = s.Queue.InsertTx(ctx, tx, githubFanoutArgs{delivery}, &river.InsertOpts{MaxAttempts: 100}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type githubFanoutWorker struct {
	river.WorkerDefaults[githubFanoutArgs]
	service *Service
}

func (w *githubFanoutWorker) Work(ctx context.Context, job *river.Job[githubFanoutArgs]) error {
	s := w.service
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var cursor uuid.UUID
	var completed bool
	e = tx.QueryRow(ctx, "SELECT event,cursor,completed FROM github_app_deliveries WHERE delivery_id=$1 FOR UPDATE", job.Args.Delivery).Scan(&raw, &cursor, &completed)
	if errors.Is(e, pgx.ErrNoRows) || completed {
		return nil
	}
	if e != nil {
		return e
	}
	var event githubEvent
	if e = json.Unmarshal(raw, &event); e != nil {
		return e
	}
	if s.GitHub == nil {
		return errors.New("GitHub App is not configured")
	}
	if event.App != s.GitHub.Config.ID {
		_, e = tx.Exec(ctx, "UPDATE github_app_deliveries SET completed=true WHERE delivery_id=$1", job.Args.Delivery)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}

	rows, e := tx.Query(ctx, `SELECT workspace_id,connection_id,generation FROM github_app_connections WHERE app_id=$1 AND installation_id=$2 AND active AND ($3::bigint=0 OR repository_id=$3) AND connection_id>$4 ORDER BY connection_id LIMIT 100`, s.GitHub.Config.ID, event.Installation, event.Repository, cursor)
	if e != nil {
		return e
	}
	entries := []githubReconcileArgs{}
	for rows.Next() {
		var a githubReconcileArgs
		if e = rows.Scan(&a.Workspace, &a.Connection, &a.Generation); e != nil {
			rows.Close()
			return e
		}
		entries = append(entries, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, a := range entries {
		if event.Kind == "push" {
			if e = s.enqueueGitRefresh(ctx, tx, a.Workspace, a.Connection, pipeline.Push{Delivery: job.Args.Delivery, Branch: event.Branch, Commit: event.Commit}); e != nil {
				return e
			}
		} else {
			// Separate durable events cannot be lost behind an already-running check.
			if _, e = s.Queue.InsertTx(ctx, tx, a, &river.InsertOpts{MaxAttempts: 100}); e != nil {
				return e
			}
		}
		cursor = a.Connection
	}
	if len(entries) == 100 {
		if _, e = tx.Exec(ctx, "UPDATE github_app_deliveries SET cursor=$2 WHERE delivery_id=$1", job.Args.Delivery, cursor); e != nil {
			return e
		}
		if _, e = s.Queue.InsertTx(ctx, tx, job.Args, &river.InsertOpts{MaxAttempts: 100}); e != nil {
			return e
		}
	} else {
		if _, e = tx.Exec(ctx, "UPDATE github_app_deliveries SET cursor=$2,completed=true WHERE delivery_id=$1", job.Args.Delivery, cursor); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

type githubReconcileWorker struct {
	river.WorkerDefaults[githubReconcileArgs]
	service *Service
}

func (w *githubReconcileWorker) Work(ctx context.Context, job *river.Job[githubReconcileArgs]) error {
	s := w.service
	a := job.Args
	if s.GitHub == nil {
		return errors.New("GitHub App is not configured")
	}
	var installation int64
	var repository string
	e := s.Store.Pool.QueryRow(ctx, `SELECT a.installation_id,c.configuration->>'repository' FROM github_app_connections a JOIN git_connections c ON c.workspace_id=a.workspace_id AND c.id=a.connection_id WHERE a.workspace_id=$1 AND a.connection_id=$2 AND a.generation=$3 AND a.active AND a.app_id=$4`, a.Workspace, a.Connection, a.Generation, s.GitHub.Config.ID).Scan(&installation, &repository)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	e = s.GitHub.Authority(ctx, installation, repository)
	if e == nil {
		return nil
	}
	if !githubapp.Revoked(e) {
		return e
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", a.Workspace); e != nil {
		return e
	}
	var current bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM github_app_connections WHERE workspace_id=$1 AND connection_id=$2 AND generation=$3 AND active)", a.Workspace, a.Connection, a.Generation).Scan(&current); e != nil {
		return e
	}
	if current {
		if e = cancelGitHubWork(ctx, tx, a.Workspace, a.Connection); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE github_app_connections SET active=false,generation=generation+1 WHERE workspace_id=$1 AND connection_id=$2 AND generation=$3 AND active", a.Workspace, a.Connection, a.Generation); e != nil {
			return e
		}
	}

	return tx.Commit(ctx)
}
