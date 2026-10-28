package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type previewArgs struct {
	Policy  uuid.UUID `json:"policy"`
	Request uuid.UUID `json:"request"`
	Number  int       `json:"number"`
}

func (previewArgs) Kind() string { return "platform_preview_v1" }

type previewSweepArgs struct{}

func (previewSweepArgs) Kind() string { return "platform_preview_sweep_v1" }

type previewWorker struct {
	river.WorkerDefaults[previewArgs]
	service *Service
}

func (w *previewWorker) Timeout(*river.Job[previewArgs]) time.Duration { return time.Minute }

type previewSweepWorker struct {
	river.WorkerDefaults[previewSweepArgs]
	service *Service
}

func (s *Service) enqueuePreview(ctx context.Context, tx pgx.Tx, args previewArgs) error {
	_, err := s.Queue.InsertTx(ctx, tx, args, &river.InsertOpts{MaxAttempts: 20})
	return err
}
func (w *previewSweepWorker) Work(ctx context.Context, _ *river.Job[previewSweepArgs]) error {
	tx, err := w.service.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id FROM preview_policies WHERE enabled AND next_discovery_at<=now() ORDER BY next_discovery_at,id LIMIT 10 FOR UPDATE SKIP LOCKED")
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, "UPDATE preview_policies SET next_discovery_at=now()+interval '1 minute' WHERE id=$1", id); err != nil {
			return err
		}
		if err = w.service.enqueuePreview(ctx, tx, previewArgs{Policy: id}); err != nil {
			return err
		}
	}
	rows, err = tx.Query(ctx, "SELECT id FROM preview_requests WHERE state<>'deleted' AND next_check_at<=now() ORDER BY next_check_at,id LIMIT 20 FOR UPDATE SKIP LOCKED")
	if err != nil {
		return err
	}
	ids = nil
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, "UPDATE preview_requests SET next_check_at=now()+interval '1 minute' WHERE id=$1", id); err != nil {
			return err
		}
		if err = w.service.enqueuePreview(ctx, tx, previewArgs{Request: id}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type previewRecord struct {
	View                             api.PullRequestPreview
	Policy                           uuid.UUID
	App                              *uuid.UUID
	PolicyGeneration                 int64
	Config                           *previewConfig
	ApprovedHead                     string
	ApprovedRepo, ApprovedGeneration int64
	Build, Plan                      *uuid.UUID
	BuiltHead                        string
	Closed                           *time.Time
	Cleanup                          bool
	ResumeAfterCancel                bool
	CleanupActor                     *uuid.UUID
	CleanupAuth, CleanupController   *int64
}

func loadPreview(ctx context.Context, tx pgx.Tx, id uuid.UUID) (previewRecord, error) {
	var r previewRecord
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT "+previewColumns+",policy_id,application_id,policy_generation,configuration,approved_head,approved_repository,approved_generation,build_id,plan_id,built_head,closed_at,cleanup_requested,cleanup_actor,cleanup_auth_version,cleanup_controller,resume_after_cancel FROM preview_requests WHERE id=$1", id).Scan(&r.View.Id, &r.View.Number, &r.View.Head, &r.View.HeadRepository, &r.View.Fork, &r.View.State, &r.View.Note, &r.View.Generation, &r.View.EnvironmentId, &r.View.OperationId, &r.View.CleanupAt, &r.Policy, &r.App, &r.PolicyGeneration, &raw, &r.ApprovedHead, &r.ApprovedRepo, &r.ApprovedGeneration, &r.Build, &r.Plan, &r.BuiltHead, &r.Closed, &r.Cleanup, &r.CleanupActor, &r.CleanupAuth, &r.CleanupController, &r.ResumeAfterCancel)
	if err == nil && len(raw) > 0 {
		var c previewConfig
		err = json.Unmarshal(raw, &c)
		r.Config = &c
	}
	return r, err
}
func (w *previewWorker) Work(ctx context.Context, job *river.Job[previewArgs]) error {
	s := w.service
	a := job.Args
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var policyID, ws uuid.UUID
	if a.Request != uuid.Nil {
		if err = tx.QueryRow(ctx, "SELECT policy_id,workspace_id FROM preview_requests WHERE id=$1", a.Request).Scan(&policyID, &ws); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
	} else {
		policyID = a.Policy
		if err = tx.QueryRow(ctx, "SELECT workspace_id FROM preview_policies WHERE id=$1", policyID).Scan(&ws); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); err != nil {
		return err
	}
	policy, err := scanPreviewPolicy(tx.QueryRow(ctx, "SELECT "+previewPolicyColumns+" FROM preview_policies WHERE id=$1 AND workspace_id=$2", policyID, ws))
	if err != nil {
		return err
	}
	if a.Request == uuid.Nil && a.Number == 0 {
		if err = s.previewAuthority(ctx, tx, policy); err != nil {
			if !errors.Is(err, domain.Forbidden) && !errors.Is(err, store.SubscriptionRequired) {
				return err
			}
			return nil
		}
		provider, _, err := s.previewProvider(ctx, tx, ws, policy.Connection)
		if err != nil {
			if _, e := tx.Exec(ctx, "UPDATE preview_policies SET note='Cannot verify repository access or discover requests. Check the Git connection; existing previews are retained.',next_discovery_at=now()+interval '1 minute' WHERE id=$1", policyID); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		var page int
		if err = tx.QueryRow(ctx, "SELECT discovery_page FROM preview_policies WHERE id=$1", policyID).Scan(&page); err != nil {
			return err
		}
		numbers, err := provider.OpenPullRequests(ctx, policy.Config.Build.Branch, page)
		if err != nil {
			if _, e := tx.Exec(ctx, "UPDATE preview_policies SET note='Cannot discover requests from the provider. Check repository access and retry after any rate limit. Existing previews are retained.',next_discovery_at=now()+interval '1 minute' WHERE id=$1", policyID); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		for _, n := range numbers {
			if err = s.enqueuePreview(ctx, tx, previewArgs{Policy: policy.ID, Number: n}); err != nil {
				return err
			}
		}
		next := page + 1
		if len(numbers) < 50 || next > 10000 {
			next = 1
		}
		if _, err = tx.Exec(ctx, "UPDATE preview_policies SET note='',discovery_page=$2,next_discovery_at=now()+interval '5 minutes' WHERE id=$1", policyID, next); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var record previewRecord
	if a.Request != uuid.Nil {
		record, err = loadPreview(ctx, tx, a.Request)
	} else {
		var id uuid.UUID
		err = tx.QueryRow(ctx, "SELECT id FROM preview_requests WHERE policy_id=$1 AND number=$2", policy.ID, a.Number).Scan(&id)
		if err == nil {
			record, err = loadPreview(ctx, tx, id)
		}
	}
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if exists && record.View.State == "deleting" {
		if err = s.reconcilePreviewCleanup(ctx, tx, policy, record); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	authority := s.previewAuthority(ctx, tx, policy)
	if exists && record.Cleanup {
		if err = s.reconcilePreviewCleanup(ctx, tx, policy, record); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if authority != nil {
		if !errors.Is(authority, domain.Forbidden) && !errors.Is(authority, store.SubscriptionRequired) {
			return authority
		}
		if exists {
			if err = s.cancelPreviewEnvironment(ctx, tx, ws, record.View.EnvironmentId); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE preview_requests SET state=CASE WHEN state='deleted' THEN state ELSE 'blocked' END,note='Preview policy needs owner approval.',next_check_at=now()+interval '5 minutes' WHERE id=$1", record.View.Id)
			if err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	provider, connection, err := s.previewProvider(ctx, tx, ws, policy.Connection)
	if err != nil {
		if exists {
			if e := s.previewState(ctx, tx, record.View.Id, "blocked", "Cannot verify the request with its provider. Existing preview resources are retained; check the Git connection.", time.Minute); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		return err
	}
	number := a.Number
	if exists {
		number = record.View.Number
	}
	request, err := provider.PullRequest(ctx, number)
	if err != nil {
		if exists {
			if e := s.previewState(ctx, tx, record.View.Id, "blocked", "Cannot verify the request with its provider. Existing preview resources are retained; check the Git connection.", time.Minute); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		return err
	}
	if !exists {
		if !request.Open || request.BaseBranch != policy.Config.Build.Branch {
			return nil
		}
		var waiting int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM preview_requests WHERE workspace_id=$1 AND environment_id IS NULL AND state<>'deleted'", ws).Scan(&waiting); err != nil {
			return err
		}
		if waiting >= 50 {
			return nil
		}
		id := uuid.New()
		if _, err = tx.Exec(ctx, "INSERT INTO preview_requests(id,workspace_id,policy_id,number) VALUES($1,$2,$3,$4)", id, ws, policy.ID, number); err != nil {
			return err
		}
		record, err = loadPreview(ctx, tx, id)
		if err != nil {
			return err
		}
	}
	if err = s.reconcilePreview(ctx, tx, policy, &record, request, connection); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) previewState(ctx context.Context, tx pgx.Tx, id uuid.UUID, state, note string, delay time.Duration) error {
	_, err := tx.Exec(ctx, "UPDATE preview_requests SET state=$2,note=$3,updated_at=now(),next_check_at=now()+$4::interval WHERE id=$1", id, state, note, delay.String())
	return err
}
func (s *Service) cancelPreviewEnvironment(ctx context.Context, tx pgx.Tx, ws uuid.UUID, env *uuid.UUID) error {
	if env == nil {
		return nil
	}
	_, err := tx.Exec(ctx, "UPDATE execution_operations SET cancel_requested=true WHERE workspace_id=$2 AND environment_id=$1 AND state NOT IN ('succeeded','failed','cancelled') AND NOT COALESCE(command->'visual'->>'action'='preview-cleanup',false)", *env, ws)
	return err
}
func previewBusy(ctx context.Context, tx pgx.Tx, ws uuid.UUID, env *uuid.UUID) (bool, error) {
	if env == nil {
		return false, nil
	}
	var busy bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$2 AND environment_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))", *env, ws).Scan(&busy)
	return busy, err
}
func (s *Service) reconcilePreview(ctx context.Context, tx pgx.Tx, p previewPolicy, r *previewRecord, pr pipeline.PullRequest, connection pipeline.Connection) error {
	id := r.View.Id
	if pr.Open && pr.BaseBranch != p.Config.Build.Branch {
		if err := s.cancelPreviewEnvironment(ctx, tx, p.Workspace, r.View.EnvironmentId); err != nil {
			return err
		}
		return s.previewState(ctx, tx, id, "blocked", "Target branch changed; retained preview needs owner review.", 5*time.Minute)
	}
	if !pr.Open {
		if err := s.cancelPreviewEnvironment(ctx, tx, p.Workspace, r.View.EnvironmentId); err != nil {
			return err
		}
		if r.View.State == "deleted" {
			return s.previewState(ctx, tx, id, "deleted", "", 5*time.Minute)
		}
		if r.View.EnvironmentId == nil {
			return s.previewState(ctx, tx, id, "deleted", "Request closed before resources were allocated.", 5*time.Minute)
		}
		grace := r.Config.Grace
		if r.Closed == nil {
			var due *time.Time
			if grace != nil {
				v := time.Now().UTC().Add(time.Duration(*grace) * time.Hour)
				due = &v
			}
			if _, err := tx.Exec(ctx, "UPDATE preview_requests r SET closed_at=now(),cleanup_at=$2,resume_after_cancel=EXISTS(SELECT 1 FROM execution_operations o WHERE o.workspace_id=r.workspace_id AND o.environment_id=r.environment_id AND o.state NOT IN ('succeeded','failed','cancelled')) WHERE r.id=$1", id, due); err != nil {
				return err
			}
			r.View.CleanupAt = due
		}
		if r.View.CleanupAt != nil && !time.Now().Before(*r.View.CleanupAt) {
			return s.reconcilePreviewCleanup(ctx, tx, p, *r)
		}
		return s.previewState(ctx, tx, id, "closing", "Request closed; preview data is retained until its cleanup deadline.", time.Minute)
	}
	if r.Closed != nil {

		if _, err := tx.Exec(ctx, "UPDATE preview_requests SET closed_at=NULL,cleanup_at=NULL,cleanup_requested=false WHERE id=$1", id); err != nil {
			return err
		}
	}
	if r.View.State == "deleted" {
		if _, err := tx.Exec(ctx, "UPDATE preview_requests SET environment_id=NULL,application_id=NULL,configuration=NULL,build_id=NULL,plan_id=NULL,operation_id=NULL,built_head='',cleanup_requested=false WHERE id=$1", id); err != nil {
			return err
		}
		r.View.EnvironmentId = nil
		r.App = nil
		r.Config = nil
		r.Build = nil
		r.Plan = nil
		r.View.OperationId = nil
		r.BuiltHead = ""
	}
	changed := r.View.Head != pr.Head || r.View.HeadRepository != pr.HeadRepository
	if changed {
		if err := s.cancelPreviewEnvironment(ctx, tx, p.Workspace, r.View.EnvironmentId); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE preview_requests SET head=$2,head_repository=$3,fork=$4,next_check_at=now() WHERE id=$1", id, pr.Head, pr.HeadRepository, pr.Fork); err != nil {
			return err
		}
		r.View.Head = pr.Head
		r.View.HeadRepository = pr.HeadRepository
		r.View.Fork = pr.Fork
	}
	if pr.Fork && (r.ApprovedHead != pr.Head || r.ApprovedRepo != pr.HeadRepository || r.ApprovedGeneration != p.Generation) {
		return s.previewState(ctx, tx, id, "approval_required", "An owner must approve this exact fork commit before it receives preview access.", 5*time.Minute)
	}
	busy, err := previewBusy(ctx, tx, p.Workspace, r.View.EnvironmentId)
	if err != nil {
		return err
	}
	if busy {
		return s.previewState(ctx, tx, id, r.View.State, "Waiting for the assigned agent to finish or acknowledge cancellation.", time.Minute)
	}
	if r.View.EnvironmentId == nil {
		var active, serverActive int
		if err = tx.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE p.server_id=$2) FROM preview_requests r JOIN preview_policies p ON p.id=r.policy_id WHERE r.workspace_id=$1 AND r.environment_id IS NOT NULL AND r.state<>'deleted'", p.Workspace, p.Server).Scan(&active, &serverActive); err != nil {
			return err
		}
		var own int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM preview_requests WHERE policy_id=$1 AND environment_id IS NOT NULL AND state<>'deleted'", p.ID).Scan(&own); err != nil {
			return err
		}
		if active >= 20 || serverActive >= 10 || own >= p.Config.MaxActive {
			return s.previewState(ctx, tx, id, "queued", "Waiting for an available preview resource slot.", time.Minute)
		}
		if err = s.createPreviewEnvironment(ctx, tx, p, r); err != nil {
			return err
		}
	}
	if r.Config == nil || r.PolicyGeneration != p.Generation {
		return s.previewState(ctx, tx, id, "blocked", "The preview's frozen policy needs owner review.", 5*time.Minute)
	}
	var resetBuild, resetPlan bool
	if err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND id=$2 AND (($4 AND state='cancelled') OR ($5 AND state='failed'))),
 EXISTS(SELECT 1 FROM visual_requests v JOIN execution_operations o ON (o.id=v.operation_id OR o.visual_plan_id=v.id) WHERE v.workspace_id=$1 AND v.id=$3 AND (($4 AND o.state='cancelled') OR ($5 AND o.state='failed')))`, p.Workspace, r.Build, r.Plan, r.ResumeAfterCancel || r.Closed != nil, r.Closed != nil).Scan(&resetBuild, &resetPlan); err != nil {
		return err
	}
	if resetBuild || resetPlan {
		if resetBuild {
			r.Build = nil
			r.BuiltHead = ""
		}
		r.Plan = nil
		r.View.OperationId = nil
		if _, err = tx.Exec(ctx, "UPDATE preview_requests SET build_id=$2,built_head=$3,plan_id=NULL,operation_id=NULL WHERE id=$1", id, r.Build, r.BuiltHead); err != nil {
			return err
		}
	}

	if r.ResumeAfterCancel {
		if _, err = tx.Exec(ctx, "UPDATE preview_requests SET resume_after_cancel=false WHERE id=$1", id); err != nil {
			return err
		}
	}

	if r.BuiltHead != pr.Head || r.Build == nil {
		var builds int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM preview_requests r JOIN execution_operations o ON o.id=r.build_id WHERE r.policy_id=$1 AND o.state NOT IN ('succeeded','failed','cancelled')", p.ID).Scan(&builds); err != nil {
			return err
		}
		if builds >= r.Config.MaxBuilds {
			return s.previewState(ctx, tx, id, "queued", "Waiting for an available preview build slot.", time.Minute)
		}
		v, err := loadVisualEnvironment(ctx, tx, p.Workspace, p.Project, *r.View.EnvironmentId)
		if err != nil {
			return err
		}
		cfg := r.Config.Build
		hash := buildInputHash(connection, pr.Head, cfg)
		op, err := s.insertPipeline(ctx, tx, p.Workspace, *r.View.EnvironmentId, uuid.New(), p.Server, pipeline.Command{Preview: &pipeline.PreviewSource{RequestID: id, Generation: r.View.Generation, Number: pr.Number, HeadRepository: pr.HeadRepository}, Action: "build-preview", Connection: connection, Commit: pr.Head, Branch: cfg.Branch, RevisionID: v.Revision, UnitID: v.Unit, AppID: *r.App, Build: &cfg, InputHash: hash, SubmittedAt: time.Now().UTC()})
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(cfg)
		if _, err = tx.Exec(ctx, "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,policy_actor) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(workspace_id,environment_id,application_id) DO UPDATE SET configuration=EXCLUDED.configuration,input_hash=EXCLUDED.input_hash,requested_commit=EXCLUDED.requested_commit", p.Workspace, *r.View.EnvironmentId, *r.App, p.Connection, raw, hash, pr.Head, p.Actor); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE preview_requests SET build_id=$2,built_head=$3,plan_id=NULL,operation_id=$2 WHERE id=$1", id, op.Id, pr.Head); err != nil {
			return err
		}
		return s.previewState(ctx, tx, id, "building", "Building the verified request commit.", time.Minute)
	}
	var buildState string
	if err = tx.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", *r.Build).Scan(&buildState); err != nil {
		return err
	}
	if buildState != "succeeded" {
		return s.previewState(ctx, tx, id, "failed", "Preview build failed or was cancelled. Push another commit to retry.", 5*time.Minute)
	}
	if r.Plan == nil {
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT identity FROM build_artifacts WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", p.Workspace, *r.View.EnvironmentId, *r.Build).Scan(&raw); err != nil {
			return err
		}
		var artifact pipeline.Artifact
		if err = json.Unmarshal(raw, &artifact); err != nil {
			return err
		}
		v, err := loadVisualEnvironment(ctx, tx, p.Workspace, p.Project, *r.View.EnvironmentId)
		if err != nil {
			return err
		}
		if err = promoteSpecification(ctx, &v.Spec, *r.App, artifact); err != nil {
			return err
		}
		rev, err := s.writePipelineRevision(ctx, tx, p.Workspace, *r.View.EnvironmentId, v.Revision, p.Actor, v.Spec)
		if err != nil {
			return err
		}
		status, err := s.createVisualPlan(ctx, tx, p.Workspace, p.Project, *r.View.EnvironmentId, api.VisualPlanRequest{BaseRevisionId: rev, ServerId: p.Server, AllowHostChanges: true})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE preview_requests SET plan_id=$2,operation_id=$3 WHERE id=$1", id, status.ID, status.OperationID); err != nil {
			return err
		}
		return s.previewState(ctx, tx, id, "deploying", "Resolving preview images and runtime state.", time.Minute)
	}
	var resolution string
	if err = tx.QueryRow(ctx, "SELECT o.state FROM visual_requests r JOIN execution_operations o ON o.id=r.operation_id WHERE r.id=$1", *r.Plan).Scan(&resolution); err != nil {
		return err
	}
	if resolution != "succeeded" {
		return s.previewState(ctx, tx, id, "failed", "Preview image resolution failed. Inspect its operation.", 5*time.Minute)
	}
	if r.View.State == "ready" {
		return s.previewState(ctx, tx, id, "ready", "", 5*time.Minute)
	}
	var deployed *uuid.UUID
	err = tx.QueryRow(ctx, "SELECT id FROM execution_operations WHERE visual_plan_id=$1", *r.Plan).Scan(&deployed)
	if errors.Is(err, pgx.ErrNoRows) {
		op, e := s.applyVisualPlan(ctx, tx, p.Workspace, p.Project, *r.View.EnvironmentId, *r.Plan, uuid.New(), true)
		if e != nil {
			return e
		}
		if _, err = tx.Exec(ctx, "UPDATE preview_requests SET operation_id=$2 WHERE id=$1", id, op.Id); err != nil {
			return err
		}
		return s.previewState(ctx, tx, id, "deploying", "Deploying the verified preview.", time.Minute)
	}
	if err != nil {
		return err
	}
	var state string
	if err = tx.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", *deployed).Scan(&state); err != nil {
		return err
	}
	if state != "succeeded" {
		return s.previewState(ctx, tx, id, "failed", "Preview deployment needs attention. Inspect its operation.", 5*time.Minute)
	}
	return s.previewState(ctx, tx, id, "ready", "", 5*time.Minute)
}
func (s *Service) createPreviewEnvironment(ctx context.Context, tx pgx.Tx, p previewPolicy, r *previewRecord) error {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT specification FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", p.Workspace, p.Template, p.Revision).Scan(&raw); err != nil {
		return err
	}
	template, _, err := domain.ParseSpec(raw)
	if err != nil {
		return err
	}
	env, rev, unit := uuid.New(), uuid.New(), uuid.New()
	q := db.New(tx)
	if _, err = q.InsertEnvironment(ctx, db.InsertEnvironmentParams{WorkspaceID: p.Workspace, ProjectID: p.Project, ID: env, Name: fmt.Sprintf("Preview %s #%d", r.View.Id, r.View.Generation+1), DraftRevisionID: rev, ConfigurationOwner: "dashboard", Purpose: "preview"}); err != nil {
		return err
	}
	ids := map[uuid.UUID]uuid.UUID{}
	for _, a := range template.Applications {
		ids[a.ID] = uuid.New()
	}
	for _, v := range template.Volumes {
		ids[v.ID] = uuid.New()
	}
	for _, b := range template.Bindings {
		ids[b.ID] = uuid.New()
	}
	for _, route := range template.Routes {
		ids[route.ID] = uuid.New()
	}
	passwords := map[uuid.UUID]bool{}
	for _, a := range template.Applications {
		if a.Postgres != nil {
			passwords[a.Postgres.PasswordSecretVersionID] = true
		}
	}
	secrets := map[uuid.UUID]uuid.UUID{}
	for _, old := range domain.SecretReferences(template) {
		var value string
		if passwords[old] {
			value, err = cryptobox.Token()
		} else {
			var v db.SecretVersion
			v, err = q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: p.Workspace, EnvironmentID: p.Template, ID: old})
			if err == nil {
				var plain []byte
				plain, err = s.Store.Keys.Open(v.EncryptedValue, store.SecretIdentity(p.Workspace, old))
				value = string(plain)
				clear(plain)
			}
		}
		if err != nil {
			return err
		}
		sid, id := uuid.New(), uuid.New()
		if err = q.InsertSecret(ctx, db.InsertSecretParams{WorkspaceID: p.Workspace, EnvironmentID: env, ID: sid, Name: "Preview " + old.String()}); err != nil {
			return err
		}
		cipher, err := s.Store.Keys.Seal([]byte(value), store.SecretIdentity(p.Workspace, id))
		if err != nil {
			return err
		}
		if _, err = q.InsertSecretVersion(ctx, db.InsertSecretVersionParams{WorkspaceID: p.Workspace, SecretID: sid, ID: id, EncryptedValue: cipher, AuthorID: p.Actor}); err != nil {
			return err
		}
		secrets[old] = id
	}
	spec, err := domain.RemapPreview(template, ids, secrets, func(app uuid.UUID) string {
		if !p.Config.Domains {
			return ""
		}
		return domain.ApplicationHostname(s.Store.ApplicationDomain, p.Workspace, env, app, p.Server)
	})
	if err != nil {
		return err
	}
	raw, err = json.Marshal(spec)
	if err != nil {
		return err
	}
	if _, err = q.InsertRevision(ctx, db.InsertRevisionParams{WorkspaceID: p.Workspace, EnvironmentID: env, ID: rev, AuthorID: p.Actor, Specification: raw, ContentHash: hash(string(raw))}); err != nil {
		return err
	}
	if err = q.InsertDeploymentUnit(ctx, db.InsertDeploymentUnitParams{WorkspaceID: p.Workspace, EnvironmentID: env, ID: unit}); err != nil {
		return err
	}
	if _, err = q.PutLayout(ctx, db.PutLayoutParams{WorkspaceID: p.Workspace, EnvironmentID: env, Positions: []byte("{}")}); err != nil {
		return err
	}
	cfg, _ := json.Marshal(p.Config)
	if _, err = tx.Exec(ctx, "UPDATE deployment_units SET server_id=$2 WHERE workspace_id=$1 AND id=$3", p.Workspace, p.Server, unit); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE preview_requests SET environment_id=$2,application_id=$3,configuration=$4,policy_generation=$5,generation=generation+1,build_id=NULL,plan_id=NULL,operation_id=NULL,built_head='' WHERE id=$1", r.View.Id, env, ids[p.App], cfg, p.Generation); err != nil {
		return err
	}
	r.View.EnvironmentId = &env
	app := ids[p.App]
	r.App = &app
	r.Config = &p.Config
	r.PolicyGeneration = p.Generation
	r.View.Generation++
	return nil
}

func (s *Service) enqueueConnectionPreviews(ctx context.Context, tx pgx.Tx, ws, connection uuid.UUID, number int) error {
	rows, err := tx.Query(ctx, "SELECT id FROM preview_policies WHERE workspace_id=$1 AND connection_id=$2 AND enabled ORDER BY id LIMIT 100", ws, connection)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.enqueuePreview(ctx, tx, previewArgs{Policy: id, Number: number}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enqueuePreviewForEnvironment(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, "SELECT id FROM preview_requests WHERE workspace_id=$2 AND environment_id=$1", env, ws).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.enqueuePreview(ctx, tx, previewArgs{Request: id})
}
