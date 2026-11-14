package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// advertiseRailpack has a runner heartbeat with build controls and the Railpack pins whose digest
// it names, as an agent of some release does.
func advertiseRailpack(t *testing.T, a authorityAgent, digest string) {
	t.Helper()
	yes, version := true, pipeline.RailpackVersion
	in := a.in
	in.Capabilities.BuildControls, in.Capabilities.RailpackVersion, in.Capabilities.RailpackInputs = &yes, &version, &digest
	agentRequest(t, a.f.server.Client(), a.f.server.URL, "/agent/v1/heartbeat", a.token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
}

func currentRailpack() string { return pipeline.RailpackFor("arm64").Digest() }

// previousRailpack is the digest of a release that moved a pinned image without a new Railpack
// version, which a runner not yet updated after management still advertises.
func previousRailpack() string {
	pins := pipeline.RailpackFor("arm64")
	pins.HelperImage = "docker.io/library/alpine:3.23@sha256:" + strings.Repeat("0", 64)
	return pins.Digest()
}

func (i *importedEnvironment) railpackBuild(runner uuid.UUID, fallback *uuid.UUID) pipeline.BuildConfig {
	return pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", Builder: "railpack", RunnerID: runner, FallbackRunnerID: fallback, Context: ".", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true,
		Budget: &pipeline.BuildBudget{CPUs: 2, MemoryMiB: 6144, SourceMiB: 1024, StateMiB: 4096, Minutes: 10}}
}

func (i *importedEnvironment) dockerfileBuild(runner uuid.UUID) pipeline.BuildConfig {
	return pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: runner, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
}

// buildTarget makes an environment with one service that builds main on every push, as saving
// one does, and returns its path and ID.
func (i *importedEnvironment) buildTarget(t *testing.T, name string, cfg pipeline.BuildConfig) (string, uuid.UUID) {
	t.Helper()
	var project api.Project
	i.b.call("POST", i.prefix+"/projects", map[string]string{"name": name}, 201, &project)
	var env api.Environment
	i.b.call("POST", i.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Target"}, 201, &env)
	path := i.prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app := uuid.New()
	image := pipelineTestImage
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
	i.b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, nil)
	raw, _ := stablejson.Marshal(cfg)
	if _, err := i.f.st.Pool.Exec(t.Context(), "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,policy_actor,policy_auth_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1)", i.ws, env.Id, app, i.source.connection.ID, raw, strings.Repeat("a", 64), strings.Repeat("a", 40), i.f.owner); err != nil {
		t.Fatal(err)
	}
	return path, env.Id
}

// onlyTargets moves the environment's source to a branch of its own, so a push to main refreshes
// only build targets.
func (i *importedEnvironment) onlyTargets(t *testing.T) {
	t.Helper()
	if _, err := i.f.st.Pool.Exec(t.Context(), `UPDATE git_sources SET configuration=jsonb_set(configuration,'{branch}','"release"') WHERE environment_id=$1`, i.env.Id); err != nil {
		t.Fatal(err)
	}
}

// keepRunnerBusy queues another environment's import on the runner, which it never takes.
func (i *importedEnvironment) keepRunnerBusy(t *testing.T) {
	t.Helper()
	var project api.Project
	i.b.call("POST", i.prefix+"/projects", map[string]string{"name": "Busy"}, 201, &project)
	var env api.Environment
	i.b.call("POST", i.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	i.b.call("PUT", i.prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "release", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, nil)
	i.runner.next()
}

func (i *importedEnvironment) pushMain(t *testing.T) string {
	t.Helper()
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, importHonest)
	i.source.push(latest)
	return latest
}

// automaticBuild waits for the environment's automatic build of commit and returns it.
func (i *importedEnvironment) automaticBuild(t *testing.T, env uuid.UUID, commit string) api.Operation {
	t.Helper()
	var id uuid.UUID
	waitFor(t, "the automatic build", 20*time.Second, func() bool {
		return i.f.st.Pool.QueryRow(t.Context(), "SELECT id FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND kind='pipeline' AND command->'pipeline'->>'action'='build' AND command->'pipeline'->>'commit'=$3", i.ws, env, commit).Scan(&id) == nil
	})
	var op api.Operation
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT server_id,state FROM execution_operations WHERE id=$1", id).Scan(&op.ServerId, &op.State); err != nil {
		t.Fatal(err)
	}
	op.Id = id
	return op
}

func (i *importedEnvironment) builds(t *testing.T, env uuid.UUID) int {
	t.Helper()
	var count int
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND kind='pipeline' AND command->'pipeline'->>'action'='build'", i.ws, env).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (i *importedEnvironment) notices(t *testing.T, path string) ([]api.BuildWait, []api.BuildRejection) {
	t.Helper()
	var waits []api.BuildWait
	var rejections []api.BuildRejection
	i.b.call("GET", path+"/build-waits", nil, 200, &waits)
	i.b.call("GET", path+"/build-rejections", nil, 200, &rejections)
	return waits, rejections
}

// A busy runner's fallback on other Railpack pins than the build needs, as one not yet updated
// after management, isn't ready: the build queues on its runner rather than being refused for the
// fallback, or stopping the refresh.
func TestBusyRunnerTakesTheBuildPastAFallbackOnOtherRailpackPins(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	i.onlyTargets(t)
	fallbackID, fallback := i.addRunner(t)
	advertiseRailpack(t, fallback, previousRailpack())
	path, env := i.buildTarget(t, "Railpack", i.railpackBuild(i.runnerID, &fallbackID))
	i.keepRunnerBusy(t)
	// After its poll, which reports what it enrolled with.
	advertiseRailpack(t, i.runner, currentRailpack())

	latest := i.pushMain(t)
	if build := i.automaticBuild(t, env, latest); build.ServerId != i.runnerID || build.State != "queued" {
		t.Fatalf("the build did not queue on its runner: server %s, state %s", build.ServerId, build.State)
	}
	if waits, rejections := i.notices(t, path); len(waits) != 0 || len(rejections) != 0 {
		t.Fatalf("a fallback on other pins held the build back: %+v %+v", waits, rejections)
	}
	i.refreshEnded(t)
}

// A target whose runner is gone is rejected alone: the branch's other targets still build in the
// same refresh, which then ends rather than retrying.
func TestOneRefusedTargetLeavesTheBranchsOtherTargetsBuilding(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	i.onlyTargets(t)
	goneID, _ := i.addRunner(t)
	refusedPath, refusedEnv := i.buildTarget(t, "Refused", i.dockerfileBuild(goneID))
	i.b.call("POST", i.prefix+"/servers/"+goneID.String()+"/revoke", nil, 204, nil)
	_, first := i.buildTarget(t, "First", i.dockerfileBuild(i.runnerID))
	_, second := i.buildTarget(t, "Second", i.dockerfileBuild(i.runnerID))

	latest := i.pushMain(t)
	for _, env := range []uuid.UUID{first, second} {
		if build := i.automaticBuild(t, env, latest); build.ServerId != i.runnerID {
			t.Fatalf("another target's build went to %s", build.ServerId)
		}
	}
	i.refreshEnded(t)
	waits, rejections := i.notices(t, refusedPath)
	if len(waits) != 0 || len(rejections) != 1 || rejections[0].Reason != "runner_unavailable" || rejections[0].Commit != latest {
		t.Fatalf("the refused target does not say why: %+v %+v", waits, rejections)
	}
	if i.builds(t, refusedEnv) != 0 {
		t.Fatal("a target with no runner was built")
	}
}

// waitsForUpdate pushes a commit whose Railpack build needs its runner updated, and returns once
// the target shows it waits for that runner.
func (i *importedEnvironment) waitsForUpdate(t *testing.T) (string, uuid.UUID, string) {
	t.Helper()
	i.onlyTargets(t)
	advertiseRailpack(t, i.runner, previousRailpack())
	path, env := i.buildTarget(t, "Railpack", i.railpackBuild(i.runnerID, nil))
	latest := i.pushMain(t)
	waitFor(t, "the build to wait for its runner", 20*time.Second, func() bool {
		waits, _ := i.notices(t, path)
		return len(waits) == 1
	})
	waits, rejections := i.notices(t, path)
	if w := waits[0]; w.Commit != latest || w.Reason != "runner_update" || w.RunnerId != i.runnerID || len(rejections) != 0 {
		t.Fatalf("the waiting build does not name its runner: %+v %+v", waits, rejections)
	}
	if i.builds(t, env) != 0 {
		t.Fatal("a build was sent to a runner on other Railpack pins")
	}
	// It waits until a day after its refresh began, as it says.
	var began time.Time
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT created_at FROM river_job WHERE kind='platform_git_refresh_v1' ORDER BY id DESC LIMIT 1").Scan(&began); err != nil {
		t.Fatal(err)
	}
	if !waits[0].Until.Equal(began.Add(24 * time.Hour)) {
		t.Fatalf("the wait ends at %s, not a day after its refresh began at %s", waits[0].Until, began)
	}
	return path, env, latest
}

// Between upgrading management and its runners, an automatic Railpack build waits for its runner
// to be updated, showing which runner, and starts once it is.
func TestBuildWaitsForItsRunnerToBeUpdated(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	path, env, latest := i.waitsForUpdate(t)

	advertiseRailpack(t, i.runner, currentRailpack())
	i.retryRefresh(t)
	if build := i.automaticBuild(t, env, latest); build.ServerId != i.runnerID {
		t.Fatalf("the build went to %s", build.ServerId)
	}
	if waits, rejections := i.notices(t, path); len(waits) != 0 || len(rejections) != 0 {
		t.Fatalf("a started build still shows a wait: %+v %+v", waits, rejections)
	}
	i.refreshEnded(t)
}

// A build whose runner isn't updated within a day is rejected as having no runner, which says why,
// and the wait ends with it.
func TestBuildWaitingADayForItsRunnerIsRejected(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	path, env, latest := i.waitsForUpdate(t)

	// The job's day began 25 hours ago. Its state is left alone, as it may still be saving its wait.
	if _, err := i.f.st.Pool.Exec(t.Context(), "UPDATE river_job SET created_at=now()-interval '25 hours' WHERE kind='platform_git_refresh_v1'"); err != nil {
		t.Fatal(err)
	}
	i.retryRefresh(t)
	i.refreshEnded(t)
	waits, rejections := i.notices(t, path)
	if len(waits) != 0 || len(rejections) != 1 || rejections[0].Reason != "runner_unavailable" || rejections[0].Commit != latest {
		t.Fatalf("a build that waited a day was not rejected: %+v %+v", waits, rejections)
	}
	if i.builds(t, env) != 0 {
		t.Fatal("a build was sent to a runner on other Railpack pins")
	}
}

// A target the refresh stops looking at while it waits, as when the owner who approved it loses
// that standing, has its wait ended with the refresh, rather than shown for good.
func TestAWaitEndsWithTheRefreshThatStoppedLookingAtIt(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	path, _, _ := i.waitsForUpdate(t)

	if _, err := i.f.st.Pool.Exec(t.Context(), "UPDATE build_targets SET policy_auth_version=policy_auth_version+1000 WHERE workspace_id=$1 AND waiting_commit IS NOT NULL", i.ws); err != nil {
		t.Fatal(err)
	}
	// A later push joins the waiting refresh, which ends at that commit, not the one it waited at.
	next := strings.Repeat("f", 40)
	i.source.tree.record(next, importHonest)
	i.source.push(next)
	i.retryRefresh(t)
	i.refreshEnded(t)
	if waits, rejections := i.notices(t, path); len(waits) != 0 || len(rejections) != 0 {
		t.Fatalf("a target the refresh no longer builds still shows a wait: %+v %+v", waits, rejections)
	}
	var left int
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM build_targets WHERE workspace_id=$1 AND waiting_commit IS NOT NULL", i.ws).Scan(&left); err != nil || left != 0 {
		t.Fatal("the wait was not ended", left, err)
	}
}
