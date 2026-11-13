package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
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

// enqueueAutomatic records an automatic deployment on the approval of actor, given with its
// authentication version approved, nil when the policy's approval was withdrawn. The worker deploys
// only while that approval still holds.
func (s *Service) enqueueAutomatic(ctx context.Context, tx pgx.Tx, run, ws, env, revision, actor uuid.UUID, approved *int64) error {
	if err := s.Store.ManagementAdmission(ctx, tx, ws); err != nil {
		if errors.Is(err, store.SubscriptionRequired) {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO pipeline_auto_deployments(id,workspace_id,environment_id,revision_id,actor_id,actor_auth_version) VALUES($1,$2,$3,$4,$5,$6)", run, ws, env, revision, actor, approved); err != nil {
		return err
	}
	_, err := s.Queue.InsertTx(ctx, tx, automaticArgs{run}, &river.InsertOpts{MaxAttempts: 100})
	return err
}
func (w *automaticWorker) Work(ctx context.Context, job *river.Job[automaticArgs]) error {
	ws, err := w.service.Store.Queries.AutomaticDeploymentWorkspace(ctx, job.Args.ID)
	if err != nil {
		return err
	}
	return w.service.withGit(ctx, ws, func(git *gitAnswers) error { return w.work(ctx, git, job, ws) })
}

// work runs under the workspace turn. The branch check happens between passes, so the pass
// that plans and deploys re-reads the draft, policy, owner and subscription it was made for.
func (w *automaticWorker) work(ctx context.Context, git *gitAnswers, job *river.Job[automaticArgs], ws uuid.UUID) error {
	var env, revision, actor, pid uuid.UUID
	var planID *uuid.UUID
	var state string
	if err := w.service.Store.Pool.QueryRow(ctx, "SELECT environment_id,revision_id,actor_id,plan_id,state FROM pipeline_auto_deployments WHERE id=$1", job.Args.ID).Scan(&env, &revision, &actor, &planID, &state); err != nil {
		return err
	}
	if state == "queued" || state == "blocked" {
		return nil
	}
	tx, done, err := w.service.Store.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		return err
	}
	defer done()
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
	var approved bool
	if approved, err = db.New(tx).AutomaticDeploymentApproved(ctx, db.AutomaticDeploymentApprovedParams{WorkspaceID: ws, ID: job.Args.ID}); err != nil {
		return err
	}
	if !approved {
		return stop("The owner who approved automatic deployment no longer approves it: they left, are no longer an owner, or changed their password or two-factor settings. An owner can approve the policy again and deploy this build.")
	}
	if err = tx.QueryRow(ctx, "SELECT project_id FROM environments WHERE workspace_id=$1 AND id=$2", ws, env).Scan(&pid); err != nil {
		return err
	}
	v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
	if err != nil {
		return err
	}
	if v.State != "active" {
		return stop("This environment was retired, so nothing is deployed to it automatically.")
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
	head, err := git.head(ctx, tx, run.Connection.ID, run.Branch)
	if err != nil {
		return err
	}
	if head.Err != nil {
		if head.Setup {
			return stop("Cannot authenticate the configured source; review the connection.")
		}
		return stop("Cannot verify the current branch; review the connection.")
	}
	if head.Value != run.Commit {
		return stop("The branch no longer points at this commit; this deployment was superseded.")
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
	own, err := automaticOwnChangeOnly(ctx, tx, ws, env, job.Args.ID, run, appliedRaw, v.Spec)
	if err != nil {
		return err
	}
	if !own {
		return stop("Not deployed: the draft also holds changes that were never deployed, such as saved variables, routes or services, or an image from an earlier run that did not deploy. This run's image or source is saved in the draft. Prepare and apply a deployment plan to review everything in the draft; automatic deployment then resumes with the next push.")
	}
	elevated, err := automaticChanges(previous, v.Spec)
	if err != nil {
		return stop(automaticReviewNote(err, "Automatic deployment needs interactive review for stateful changes or new host permissions."))
	}
	if planID == nil {
		// A savepoint keeps a failed admission from retaining partial claims or plans.
		nested, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		status, err := w.service.createVisualPlan(ctx, nested, ws, pid, env, api.VisualPlanRequest{BaseRevisionId: revision, ServerId: *v.Server}, false)
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
	// Every service is compared with the images the last reviewed deployment ran, so a moved tag
	// never goes out unreviewed alongside this run's change.
	var appliedID uuid.UUID
	if err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id IS NOT NULL", ws, env).Scan(&appliedID); err != nil {
		if err == pgx.ErrNoRows {
			return stop("Not deployed: there is no recorded reviewed deployment to compare this run's images with. Prepare and apply a deployment plan; automatic deployment then resumes with the next push.")
		}
		return err
	}
	applied, err := readPlan(ctx, tx, ws, env, appliedID)
	if err != nil {
		return err
	}
	if applied.RevisionID != *v.Applied {
		return stop("Not deployed: the running deployment does not match the last reviewed configuration. Prepare and apply a deployment plan; automatic deployment then resumes with the next push.")
	}
	target := uuid.Nil
	if run.Action == "build" {
		target = run.AppID
	}
	if err = automaticImages(previous, v.Spec, elevated, target, planImages(applied), planImages(candidate)); err != nil {
		return stop(err.Error())
	}
	// The policy covers what the environment's applied deployment already has on its host,
	// nothing new. The checks above name what changed; this also stops for what the agent
	// reported when it resolved the plan, such as a proxy change.
	review, err := db.New(tx).VisualPlanHostReview(ctx, db.VisualPlanHostReviewParams{WorkspaceID: ws, EnvironmentID: env, ID: *planID})
	if err != nil {
		return err
	}
	if review && slices.Contains(candidate.Effects, deployment.UnreportedHostChangesEffect) {
		return stop("Not deployed: this server's agent does not report what deployments would change on its host, so every deployment that keeps host permissions needs an owner's review. Update the agent; automatic deployment then resumes with the next push.")
	}
	if review {
		return stop("Not deployed: this change also changes its server's host configuration. Prepare and apply a deployment plan to review and approve it; automatic deployment then resumes with the next push.")
	}
	nested, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	op, err := w.service.applyVisualPlan(ctx, nested, ws, pid, env, *planID, job.Args.ID)
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

// automaticOwnChangeOnly reports whether the draft is exactly the applied specification plus this run's
// change. The owner's policy covers a built image or merged source. Anything else in the draft, whoever
// saved it, has had no plan review, and a credential without deployment capabilities can save it. A change
// that cannot be rebuilt from the applied specification did not come from this run alone.
func automaticOwnChangeOnly(ctx context.Context, tx pgx.Tx, ws, env, id uuid.UUID, run pipeline.Command, applied []byte, draft domain.Specification) (bool, error) {
	expected, _, err := domain.ParseSpec(applied)
	if err != nil {
		return false, err
	}
	switch run.Action {
	case "import":
		// Refresh reuses the draft's entrypoints, so only the files and their origin are the commit's.
		if expected.Compose == nil {
			return false, nil
		}
		expected.Compose.Files, expected.Compose.Origin = draft.Compose.Files, draft.Compose.Origin
	case "build":
		raw, err := db.New(tx).AutomaticBuildArtifact(ctx, db.AutomaticBuildArtifactParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return false, err
		}
		var artifact pipeline.Artifact
		if err = json.Unmarshal(raw, &artifact); err != nil {
			return false, err
		}
		if err = promoteSpecification(ctx, &expected, run.AppID, artifact); err != nil {
			return false, ctx.Err()
		}
	}
	raw, err := stablejson.Marshal(expected)
	if err != nil {
		return false, err
	}
	parsed, _, err := domain.ParseSpec(raw)
	if err != nil {
		return false, nil
	}
	return deployment.Hash(parsed) == deployment.Hash(draft), nil
}

// automaticReview is a reason an automatic deployment stops for interactive review. Its note is
// shown in job history.
type automaticReview struct{ note string }

func (e *automaticReview) Error() string { return e.note }

func needsReview(format string, args ...any) error {
	return &automaticReview{note: fmt.Sprintf(format, args...)}
}

func automaticReviewNote(err error, fallback string) string {
	var review *automaticReview
	if errors.As(err, &review) {
		return review.note
	}
	return fallback
}

const resumeNote = "Prepare and apply a deployment plan to review it; automatic deployment then resumes with the next push."

func statefulService(service types.ServiceConfig) bool {
	for _, mount := range service.Volumes {
		if mount.Type == "volume" {
			return true
		}
	}
	return false
}

// Automatic policy carries forward existing permissions, never grants new ones.
// Stateful service changes require an interactive plan because image digests do not
// establish storage compatibility or migration safety. A privileged or cap_add service
// must stay exactly as reviewed: its approval covered what it ran, so a pushed commit
// that changes it would otherwise run new code with host-level access. Only published
// ports carry forward for services whose definitions change. It returns the privileged and
// cap_add services.
func automaticChanges(previous, next domain.Specification) (map[uuid.UUID]elevatedReview, error) {
	elevated := map[uuid.UUID]elevatedReview{}
	if (previous.Compose == nil) != (next.Compose == nil) {
		return nil, needsReview("Not deployed: the environment changed between Compose files and visual services. %s", resumeNote)
	}
	oldPermissions, nextPermissions := []string{}, []string{}
	var oldProject, nextProject *composeproject.Project
	if previous.Compose != nil {
		var err error
		oldProject, err = deployment.SourceProject(deployment.Plan{Specification: previous}, nil)
		if err != nil {
			return nil, err
		}
		nextProject, err = deployment.SourceProject(deployment.Plan{Specification: next}, nil)
		if err != nil {
			return nil, err
		}
		oldPermissions, nextPermissions = oldProject.Permissions, nextProject.Permissions
	}
	for _, permission := range nextPermissions {
		if !slices.Contains(oldPermissions, permission) {
			return nil, needsReview("Not deployed: this change requests a host permission that was never approved (%s). %s", permission, resumeNote)
		}
	}
	for _, port := range requiredPorts(next) {
		if !slices.Contains(requiredPorts(previous), port) {
			return nil, needsReview("Not deployed: this change needs proxy port %d, which was never approved. %s", port, resumeNote)
		}
	}
	if previous.Compose != nil {
		elevated = compareElevatedServices(oldProject, nextProject, previous, next)
		names := serviceNames(oldProject, nextProject)
		for _, name := range names {
			if review, ok := elevated[composeproject.ResourceID(next.Compose.Namespace, "service", name)]; ok && !review.Unchanged {
				return nil, needsReview("Not deployed: %s has host-level access, because it runs privileged or with added Linux capabilities or shares a volume or network with a service that does, and this change alters or removes it, such as its image, command, environment or files. Automatic deployment never changes a service with host-level access. %s", name, resumeNote)
			}
		}
		for _, name := range names {
			old, oldOK := oldProject.Model.Services[name]
			now, nowOK := nextProject.Model.Services[name]
			if _, ok := elevated[composeproject.ResourceID(next.Compose.Namespace, "service", name)]; ok || !(oldOK && statefulService(old) || nowOK && statefulService(now)) {
				continue
			}
			if oldOK && nowOK && reflect.DeepEqual(composeServiceInputs(oldProject, name, previous, false), composeServiceInputs(nextProject, name, next, false)) {
				continue
			}
			return nil, needsReview("Not deployed: %s keeps data in a volume, and this change alters or removes it, such as its image, command, environment or files. %s", name, resumeNote)
		}
	} else {
		for _, app := range previous.Applications {
			if len(app.Mounts) == 0 {
				continue
			}
			index := slices.IndexFunc(next.Applications, func(a domain.Application) bool { return a.ID == app.ID })
			if index < 0 || (!reflect.DeepEqual(app, next.Applications[index]) || !reflect.DeepEqual(statefulVisualInputs(previous, app), statefulVisualInputs(next, next.Applications[index]))) {
				return nil, needsReview("Not deployed: %s keeps data in a volume, and this change alters or removes it, or the variables and outputs it reads. %s", app.Name, resumeNote)
			}
		}
		for _, app := range next.Applications {
			if len(app.Mounts) == 0 {
				continue
			}
			index := slices.IndexFunc(previous.Applications, func(a domain.Application) bool { return a.ID == app.ID })
			if index < 0 || (!reflect.DeepEqual(app, previous.Applications[index]) || !reflect.DeepEqual(statefulVisualInputs(next, app), statefulVisualInputs(previous, previous.Applications[index]))) {
				return nil, needsReview("Not deployed: %s keeps data in a volume, and this change adds or alters it, or the variables and outputs it reads. %s", app.Name, resumeNote)
			}
		}
	}
	return elevated, nil
}

// composeServiceInputs is what decides what a Compose service runs: its merged and interpolated
// definition and the bundled files, configs and secret versions it reads. For a service with
// host-level access it also covers how it is reached: the networks and volumes it attaches to
// and the routes to it.
func composeServiceInputs(project *composeproject.Project, name string, spec domain.Specification, elevated bool) map[string]any {
	service := project.Model.Services[name]
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
	if !elevated {
		return inputs
	}
	for _, mount := range service.Volumes {
		if mount.Type == "volume" {
			inputs["volume:"+mount.Source] = project.Model.Volumes[mount.Source]
		}
	}
	for _, network := range serviceNetworks(spec, name, service) {
		inputs["network:"+network] = project.Model.Networks[network]
	}
	id := composeproject.ResourceID(spec.Compose.Namespace, "service", name)
	routes := []domain.Route{}
	for _, route := range spec.Routes {
		if route.TargetID == id {
			routes = append(routes, route)
		}
	}
	inputs["routes"] = routes
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

// automaticImages requires every service this run did not change to resolve to the image the
// last reviewed deployment ran, so a moved mutable tag never deploys unreviewed. A service the
// run changed is its build target or, for an imported commit, one whose image reference the
// commit edited. Stateful and privileged (elevated) services must keep their reviewed image
// even then.
func automaticImages(previous, next domain.Specification, elevated map[uuid.UUID]elevatedReview, target uuid.UUID, applied, candidate map[string]string) error {
	for _, app := range next.Applications {
		index := slices.IndexFunc(previous.Applications, func(a domain.Application) bool { return a.ID == app.ID })
		if index < 0 {
			// A new service is this run's change. New permissions and new stateful
			// services were already refused.
			continue
		}
		old := previous.Applications[index]
		stateful := len(old.Mounts) > 0 || len(app.Mounts) > 0
		_, privileged := elevated[app.ID]
		if !stateful && !privileged && (app.ID == target || !sameImageReference(old.Source.Image, app.Source.Image)) {
			continue
		}
		if resolvedImageStands(applied, candidate, app.ID) {
			continue
		}
		id := app.ID.String()
		switch {
		case applied[id] == "" || candidate[id] == "":
			return needsReview("Not deployed: %s has no single resolved image in both the last reviewed deployment and this plan to compare. %s", app.Name, resumeNote)
		case privileged:
			return needsReview("Not deployed: %s has host-level access, because it runs privileged or with added Linux capabilities or shares a volume or network with a service that does, and its image now resolves to a different digest than the last reviewed deployment ran. %s", app.Name, resumeNote)
		case stateful:
			return needsReview("Not deployed: %s keeps data in a volume, and its image now resolves to a different digest than the last reviewed deployment ran. %s", app.Name, resumeNote)
		default:
			return needsReview("Not deployed: the image tag for %s now points at a different image than the last reviewed deployment ran, and this run did not change it. Prepare and apply a deployment plan to review the new image; automatic deployment then resumes with the next push. Pinning the image by digest keeps its tag from moving under later deployments.", app.Name)
		}
	}
	return nil
}

// sameImageReference compares the references as written, after normalising the registry and
// an implied latest tag, so a moved tag is told apart from an edited one.
func sameImageReference(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	normal := func(value string) string {
		named, err := reference.ParseNormalizedNamed(value)
		if err != nil {
			return value
		}
		return reference.TagNameOnly(named).String()
	}
	return normal(*a) == normal(*b)
}

// planImages collects a plan's resolved images, including those of each cross-host unit. A
// service that resolves differently on two units has no single reviewed image.
func planImages(p deployment.Plan) map[string]string {
	out := map[string]string{}
	add := func(images map[string]string) {
		for id, image := range images {
			if old, ok := out[id]; ok && old != image {
				out[id] = ""
				continue
			}
			out[id] = image
		}
	}
	add(p.Images)
	if p.Distributed != nil {
		for _, unit := range p.Distributed.Units {
			add(unit.Plan.Images)
		}
	}
	return out
}
