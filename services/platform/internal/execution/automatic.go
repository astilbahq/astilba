package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"reflect"
	"slices"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type automaticArgs struct {
	ID uuid.UUID `json:"id"`
}

func (automaticArgs) Kind() string { return "platform_automatic_deployment_v1" }

type automaticWorker struct {
	river.WorkerDefaults[automaticArgs]
	service *Service
}

func (s *Service) enqueueAutomatic(ctx context.Context, tx pgx.Tx, run, ws, env, revision, actor uuid.UUID) error {
	if err := s.Store.ManagementAdmission(ctx, tx, ws); err != nil {
		if errors.Is(err, store.SubscriptionRequired) {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO pipeline_auto_deployments(id,workspace_id,environment_id,revision_id,actor_id) VALUES($1,$2,$3,$4,$5)", run, ws, env, revision, actor); err != nil {
		return err
	}
	_, err := s.Queue.InsertTx(ctx, tx, automaticArgs{run}, &river.InsertOpts{MaxAttempts: 100})
	return err
}
func (w *automaticWorker) Work(ctx context.Context, job *river.Job[automaticArgs]) error {
	tx, err := w.service.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ws, env, revision, actor, pid uuid.UUID
	var planID *uuid.UUID
	var state string
	if err = tx.QueryRow(ctx, "SELECT workspace_id,environment_id,revision_id,actor_id,plan_id,state FROM pipeline_auto_deployments WHERE id=$1", job.Args.ID).Scan(&ws, &env, &revision, &actor, &planID, &state); err != nil {
		return err
	}
	if state == "queued" || state == "blocked" {
		return nil
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); err != nil {
		return err
	}
	stop := func(note string) error {
		_, err := tx.Exec(ctx, "UPDATE pipeline_auto_deployments SET state='blocked',note=$2 WHERE id=$1", job.Args.ID, note)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err = w.service.Store.ManagementAdmission(ctx, tx, ws); err != nil {
		if errors.Is(err, store.SubscriptionRequired) {
			return stop("Subscription expired before automatic deployment.")
		}
		return err
	}
	var owner bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND account_id=$2 AND role='owner')", ws, actor).Scan(&owner); err != nil {
		return err
	}
	if !owner {
		return stop("The policy owner no longer has owner access.")
	}
	if err = tx.QueryRow(ctx, "SELECT project_id FROM environments WHERE workspace_id=$1 AND id=$2", ws, env).Scan(&pid); err != nil {
		return err
	}
	v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
	if err != nil {
		return err
	}
	// The first deployment and any new host permissions always require interactive review.
	if v.Revision != revision || v.Server == nil || v.Applied == nil {
		return stop("The draft changed, or this environment needs its first reviewed deployment.")
	}
	var policy bool
	if err = tx.QueryRow(ctx, `SELECT CASE WHEN r.configuration->>'action'='import' THEN COALESCE((g.configuration->>'autoDeploy')::boolean,false) ELSE COALESCE((b.configuration->>'autoDeploy')::boolean,false) AND b.input_hash=r.configuration->>'inputHash' AND b.requested_commit=r.configuration->>'commit' END FROM pipeline_runs r LEFT JOIN git_sources g ON g.workspace_id=r.workspace_id AND g.environment_id=r.environment_id LEFT JOIN build_targets b ON b.workspace_id=r.workspace_id AND b.environment_id=r.environment_id AND b.application_id=r.application_id WHERE r.id=$1`, job.Args.ID).Scan(&policy); err != nil {
		return err
	}
	if !policy {
		return stop("Automatic deployment was disabled.")
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT configuration FROM pipeline_runs WHERE id=$1", job.Args.ID).Scan(&raw); err != nil {
		return err
	}
	var run pipeline.Command
	if err = json.Unmarshal(raw, &run); err != nil {
		return err
	}
	provider, _, err := w.service.provider(ctx, tx, ws, run.Connection.ID)
	if err != nil {
		return stop("Cannot authenticate the configured source; review the connection.")
	}
	head, err := provider.Resolve(ctx, run.Branch)
	if err != nil {
		return stop("Cannot verify the current branch; review the connection.")
	}
	if head != run.Commit {
		return stop("A newer source commit exists; this deployment was superseded.")
	}
	var busy bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND state NOT IN ('succeeded','failed','cancelled'))", ws, env).Scan(&busy); err != nil {
		return err
	}
	if busy {
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobSnooze(2 * time.Second)
	}
	var appliedRaw []byte
	if err = tx.QueryRow(ctx, "SELECT specification FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, *v.Applied).Scan(&appliedRaw); err != nil {
		return err
	}
	previous, _, err := domain.ParseSpec(appliedRaw)
	if err != nil {
		return err
	}
	allowHost, err := automaticChanges(previous, v.Spec)
	if err != nil {
		return stop("Automatic deployment needs interactive review for stateful changes or new host permissions.")
	}
	if planID == nil {
		// A savepoint keeps a failed admission from retaining partial claims or plans.
		nested, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		status, err := w.service.createVisualPlan(ctx, nested, ws, pid, env, api.VisualPlanRequest{BaseRevisionId: revision, ServerId: *v.Server, AllowHostChanges: allowHost})
		if err != nil {
			_ = nested.Rollback(ctx)
			if _, ok := err.(*domain.Error); ok {
				return stop("Automatic deployment needs an interactive plan: " + err.Error())
			}
			return err
		}
		if err = nested.Commit(ctx); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE pipeline_auto_deployments SET plan_id=$2 WHERE id=$1", job.Args.ID, status.ID); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobSnooze(2 * time.Second)
	}
	var ready bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM visual_plans WHERE workspace_id=$1 AND environment_id=$2 AND id=$3)", ws, env, *planID).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return stop("Image resolution did not produce a deployable plan.")
	}
	candidate, err := readPlan(ctx, tx, ws, env, *planID)
	if err != nil {
		return err
	}
	stateful := false
	for _, app := range previous.Applications {
		stateful = stateful || len(app.Mounts) > 0
	}
	if stateful {
		var appliedID uuid.UUID
		if err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id IS NOT NULL", ws, env).Scan(&appliedID); err != nil {
			if err == pgx.ErrNoRows {
				return stop("Stateful services need a recorded reviewed deployment before automatic updates.")
			}
			return err
		}
		applied, err := readPlan(ctx, tx, ws, env, appliedID)
		if err != nil {
			return err
		}
		if applied.RevisionID != *v.Applied || automaticStatefulImages(previous, applied.Images, candidate.Images) != nil {
			return stop("A stateful image changed; review its deployment interactively.")
		}
	}
	nested, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	op, err := w.service.applyVisualPlan(ctx, nested, ws, pid, env, *planID, job.Args.ID, allowHost)
	if err != nil {
		_ = nested.Rollback(ctx)
		if _, ok := err.(*domain.Error); ok {
			return stop("Automatic deployment admission failed: " + err.Error())
		}
		return err
	}
	if err = nested.Commit(ctx); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE pipeline_auto_deployments SET state='queued',operation_id=$2 WHERE id=$1", job.Args.ID, op.Id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Automatic policy carries forward existing permissions, never grants new ones.
// Stateful service changes require an interactive plan because image digests do not
// establish storage compatibility or migration safety.
func automaticChanges(previous, next domain.Specification) (bool, error) {
	if (previous.Compose == nil) != (next.Compose == nil) {
		return false, domain.Invalid("automatic.authorityChanged")
	}
	oldPermissions, nextPermissions := []string{}, []string{}
	var oldProject, nextProject *composeproject.Project
	if previous.Compose != nil {
		var err error
		oldProject, err = deployment.SourceProject(deployment.Plan{Specification: previous}, nil)
		if err != nil {
			return false, err
		}
		nextProject, err = deployment.SourceProject(deployment.Plan{Specification: next}, nil)
		if err != nil {
			return false, err
		}
		oldPermissions, nextPermissions = oldProject.Permissions, nextProject.Permissions
	}
	for _, permission := range nextPermissions {
		if !slices.Contains(oldPermissions, permission) {
			return false, domain.Invalid("automatic.newHostPermissions")
		}
	}
	for _, port := range requiredPorts(next) {
		if !slices.Contains(requiredPorts(previous), port) {
			return false, domain.Invalid("automatic.newHostPermissions")
		}
	}
	if previous.Compose != nil {
		for name, service := range oldProject.Model.Services {
			stateful := false
			for _, mount := range service.Volumes {
				stateful = stateful || mount.Type == "volume"
			}
			if stateful && !reflect.DeepEqual(statefulComposeInputs(oldProject, service, previous), statefulComposeInputs(nextProject, nextProject.Model.Services[name], next)) {
				return false, domain.Invalid("automatic.statefulChangeNeedsReview")
			}
		}
		for name, service := range nextProject.Model.Services {
			for _, mount := range service.Volumes {
				if mount.Type == "volume" && !reflect.DeepEqual(statefulComposeInputs(nextProject, service, next), statefulComposeInputs(oldProject, oldProject.Model.Services[name], previous)) {
					return false, domain.Invalid("automatic.statefulChangeNeedsReview")
				}
			}
		}
	} else {
		for _, app := range previous.Applications {
			if len(app.Mounts) == 0 {
				continue
			}
			index := slices.IndexFunc(next.Applications, func(a domain.Application) bool { return a.ID == app.ID })
			if index < 0 || (!reflect.DeepEqual(app, next.Applications[index]) || !reflect.DeepEqual(statefulVisualInputs(previous, app), statefulVisualInputs(next, next.Applications[index]))) {
				return false, domain.Invalid("automatic.statefulChangeNeedsReview")
			}
		}
		for _, app := range next.Applications {
			if len(app.Mounts) == 0 {
				continue
			}
			index := slices.IndexFunc(previous.Applications, func(a domain.Application) bool { return a.ID == app.ID })
			if index < 0 || (!reflect.DeepEqual(app, previous.Applications[index]) || !reflect.DeepEqual(statefulVisualInputs(next, app), statefulVisualInputs(previous, previous.Applications[index]))) {
				return false, domain.Invalid("automatic.statefulChangeNeedsReview")
			}
		}
	}
	return len(nextPermissions) > 0, nil
}

func statefulComposeInputs(project *composeproject.Project, service types.ServiceConfig, spec domain.Specification) map[string]any {
	inputs := map[string]any{"service": service}
	for _, mount := range service.Volumes {
		if mount.Type == "bind" {
			inputs["file:"+mount.Source] = project.Files[mount.Source]
		}
	}
	for _, ref := range service.Configs {
		config := project.Model.Configs[ref.Source]
		inputs["config:"+ref.Source] = config
		if config.File != "" {
			inputs["file:"+config.File] = project.Files[config.File]
		}
	}
	for _, ref := range service.Secrets {
		secret := project.Model.Secrets[ref.Source]
		inputs["secret:"+ref.Source] = secret
		if secret.Environment != "" {
			inputs["secret-value:"+ref.Source] = domain.VariableValues(spec.Variables, nil)[secret.Environment]
		}
	}
	return inputs
}
func statefulVisualInputs(spec domain.Specification, app domain.Application) map[string]any {
	values := domain.VariableValues(spec.Variables, nil)
	for key, value := range domain.VariableValues(app.Variables, nil) {
		values[key] = value
	}
	bindings := map[string]any{}
	for _, binding := range spec.Bindings {
		if binding.TargetID != app.ID {
			continue
		}
		delete(values, binding.Variable)
		for _, source := range spec.Applications {
			if source.ID == binding.SourceID {
				output, _ := domain.BindingOutput(source, binding.Output)
				input, _ := domain.OutputVariable(spec, source, output.Variable)
				bindings[binding.Variable] = struct {
					ID       uuid.UUID
					Postgres *domain.Postgres
					Output   domain.ServiceOutput
					Input    domain.Variable
				}{source.ID, source.Postgres, output, input}
			}
		}
	}
	return map[string]any{"variables": values, "bindings": bindings}
}
func automaticStatefulImages(spec domain.Specification, applied, candidate map[string]string) error {
	for _, app := range spec.Applications {
		if len(app.Mounts) > 0 && (applied[app.ID.String()] == "" || applied[app.ID.String()] != candidate[app.ID.String()]) {
			return domain.Invalid("automatic.statefulImageNeedsReview")
		}
	}
	return nil
}
