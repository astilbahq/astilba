package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	importHonest = []composeproject.File{
		{Path: "compose.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n    env_file: config/app.env\n"},
		{Path: "config/app.env", Content: "MODE=production\n"},
	}
	importPaths = []string{"compose.yaml", "config/app.env"}
)

func withForgedService(files []composeproject.File) []composeproject.File {
	forged := append([]composeproject.File{}, files...)
	forged[0].Content += "  forged:\n    image: " + execution.TestImage + "\n"
	return forged
}

// importedEnvironment is an environment whose source imported its first commit, with a
// build-only runner and, for the deployment that imports must not reach, a separate server.
type importedEnvironment struct {
	f        *fixture
	b        *browser
	ws       uuid.UUID
	prefix   string
	path     string
	env      api.Environment
	source   *gitSource
	runnerID uuid.UUID
	runner   authorityAgent
	in       api.AgentEnrollment
	server   api.Enrollment
	deploy   authorityAgent
	draft    api.Revision
	// delivered counts the deployment server's operations before the test acts, such as the
	// reviewed deployment that reviewed records.
	delivered int
}

func newImportedEnvironment(t *testing.T, kind string) *importedEnvironment {
	t.Helper()
	f := setup(t)
	b := f.browser(t)
	i := &importedEnvironment{f: f, b: b}
	i.ws, i.prefix, i.env, i.path = composeEnvironment(t, f, b)
	i.source = newGitSourceOf(t, f, b, i.prefix, kind)
	yes := true
	enrolled, in, token := enrollment(t, f, b, i.ws, "build")
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	i.runnerID, i.in, i.runner = enrolled.Server.Id, in, authorityAgent{t, f, token, in}
	var deployIn api.AgentEnrollment
	var deployToken string
	i.server, deployIn, deployToken = enrollment(t, f, b, i.ws, "deployment")
	deployIn.Capabilities.Visual, deployIn.Capabilities.Compose = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", deployIn, 200, nil)
	i.deploy = authorityAgent{t, f, deployToken, deployIn}

	head := i.source.head.Load().(string)
	i.source.tree.record(head, importHonest)
	var op api.Operation
	b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, &op)
	i.runner.reportImport(i.runner.next(), head, importHonest)
	i.draft, _ = draftSpec(t, b, i.path)
	if i.draft.Id == i.env.DraftRevisionId {
		t.Fatal("an honest import was not saved", kind)
	}
	return i
}

// reviewed records the environment as already deployed on its deployment server, which is
// what lets a later import's policy deploy without a person.
func (i *importedEnvironment) reviewed(t *testing.T) {
	t.Helper()
	recordReviewedDeployment(t, i.f, i.ws, i.env.Id, i.draft.Id, i.server.Server.Id, nil)
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE server_id=$1", i.server.Server.Id).Scan(&i.delivered); err != nil {
		t.Fatal(err)
	}
	i.b.call("PUT", i.path+"/source/policy", map[string]bool{"autoDeploy": true}, 204, nil)
}

func (i *importedEnvironment) operation(t *testing.T, id uuid.UUID) api.Operation {
	t.Helper()
	var op api.Operation
	i.b.call("GET", i.path+"/operations/"+id.String(), nil, 200, &op)
	return op
}

// refresh starts an import of the provider's current head for an owner.
func (i *importedEnvironment) refresh(t *testing.T) api.ExecutionCommand {
	t.Helper()
	var op api.Operation
	i.b.call("POST", i.path+"/source/refresh", map[string]any{"baseRevisionId": i.draft.Id}, 202, &op)
	c := i.runner.next()
	if c.Id != op.Id {
		t.Fatal("refresh did not reach the runner")
	}
	return c
}

// requireUnchanged wants nothing from the import reached the draft, the source's record of
// what it imported, or the automatic deployment that follows an accepted one.
func (i *importedEnvironment) requireUnchanged(t *testing.T) {
	t.Helper()
	draft, spec := draftSpec(t, i.b, i.path)
	if draft.Id != i.draft.Id {
		t.Fatal("a refused import wrote a revision")
	}
	if strings.Contains(spec.Compose.Files[0].Content, "forged:") {
		t.Fatal("forged files reached the draft")
	}
	var imported string
	var runs, delivered int
	ctx := context.Background()
	if err := i.f.st.Pool.QueryRow(ctx, "SELECT imported_commit FROM git_sources WHERE workspace_id=$1 AND environment_id=$2", i.ws, i.env.Id).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	if err := i.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_auto_deployments WHERE workspace_id=$1 AND environment_id=$2", i.ws, i.env.Id).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := i.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM execution_operations WHERE server_id=$1", i.server.Server.Id).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if imported != strings.Repeat("a", 40) || runs != 0 || delivered != i.delivered {
		t.Fatalf("a refused import had effects: imported=%s automatic=%d delivered=%d", imported, runs, delivered)
	}
}

func (i *importedEnvironment) importOperations(t *testing.T, commit string) int {
	t.Helper()
	var count int
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND kind='pipeline' AND command->'pipeline'->>'action'='import' AND command->'pipeline'->>'commit'=$2", i.ws, commit).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// retryRefresh runs a waiting refresh job now rather than after its delay, and returns when
// that attempt has finished.
func (i *importedEnvironment) retryRefresh(t *testing.T) {
	t.Helper()
	var id int64
	var attempted time.Time
	waitFor(t, "the job to wait", 15*time.Second, func() bool {
		err := i.f.st.Pool.QueryRow(t.Context(), "SELECT id,attempted_at FROM river_job WHERE kind='platform_git_refresh_v1' AND state='scheduled' ORDER BY id DESC LIMIT 1").Scan(&id, &attempted)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return err == nil
	})
	if _, err := i.f.execution.Queue.JobRetry(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retried refresh", 15*time.Second, func() bool {
		// A short wait passes as available, because River promotes jobs ahead of their time.
		var finished bool
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT attempted_at>$2 AND state<>'running' FROM river_job WHERE id=$1", id, attempted).Scan(&finished); err != nil {
			t.Fatal(err)
		}
		return finished
	})
}

func (i *importedEnvironment) pendingCommit(t *testing.T, path string) string {
	t.Helper()
	var status api.GitSourceStatus
	i.b.call("GET", path+"/source", nil, 200, &status)
	return status.PendingCommit
}

// A compromised build-only runner must not choose what a deployment server runs. The forged
// result names the assigned commit and paths, so only the provider's tree can tell it apart.
func TestForgedImportIsRefusedAndHonestImportDeploys(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			i := newImportedEnvironment(t, kind)
			i.reviewed(t)
			latest := strings.Repeat("e", 40)
			next := append([]composeproject.File{{Path: "compose.yaml", Content: "# next\n" + importHonest[0].Content}}, importHonest[1])
			i.source.tree.record(latest, next)
			i.source.push(latest)
			c := i.runner.next()
			i.runner.reportImport(c, latest, withForgedService(next))
			op := i.operation(t, c.Id)
			if op.State != "failed" || !strings.Contains(op.Note, "differ from what the Git provider holds") {
				t.Fatalf("forged import was not refused: %s %q", op.State, op.Note)
			}
			i.requireUnchanged(t)

			c = i.refresh(t)
			i.runner.reportImport(c, latest, next)
			if op = i.operation(t, c.Id); op.State != "succeeded" {
				t.Fatalf("honest import was refused: %s %q", op.State, op.Note)
			}
			_, spec := draftSpec(t, i.b, i.path)
			if spec.Compose.Origin.Commit != latest || spec.Compose.Files[0].Content != next[0].Content {
				t.Fatal("honest import lost its identity or source")
			}
			resolve := i.deploy.next()
			if resolve.ServerId != i.server.Server.Id || resolve.Kind != "visual" {
				t.Fatal("an honest import did not reach automatic deployment", resolve.ServerId, resolve.Kind)
			}
		})
	}
}

func TestImportCheckComparesEverySelectedFile(t *testing.T) {
	i := newImportedEnvironment(t, "gitea")
	latest := strings.Repeat("e", 40)
	i.source.head.Store(latest)
	second := []composeproject.File{importHonest[0], {Path: "config/app.env", Content: "MODE=forged\n"}}
	i.source.tree.record(latest, importHonest)
	c := i.refresh(t)
	i.runner.reportImport(c, latest, second)
	if got := i.operation(t, c.Id); got.State != "failed" || !strings.Contains(got.Note, "differ from what the Git provider holds") {
		t.Fatalf("a second file that differs was not refused: %s %q", got.State, got.Note)
	}
	i.requireUnchanged(t)
	// A selected file the provider lacks is refused before any runner is asked, by name.
	i.source.tree.record(latest, importHonest[:1])
	var refused api.Error
	if err := json.Unmarshal(i.b.call("POST", i.path+"/source/refresh", map[string]any{"baseRevisionId": i.draft.Id}, 400, nil), &refused); err != nil || !strings.Contains(refused.Message, `"config/app.env"`) {
		t.Fatalf("a file the provider lacks was not refused by name: %q %v", refused.Message, err)
	}
	i.requireUnchanged(t)
}

// The commit's tree cannot change after admission, so a report needs no network. An outage
// must not hold the operation open, because a server runs its operations one at a time.
func TestImportReportAsksTheProviderNothing(t *testing.T) {
	i := newImportedEnvironment(t, "gitea")
	latest := strings.Repeat("e", 40)
	i.source.head.Store(latest)
	i.source.tree.record(latest, importHonest)
	c := i.refresh(t)
	i.source.outage.Store(503)
	i.runner.reportImport(c, latest, withForgedService(importHonest))
	if got := i.operation(t, c.Id); got.State != "failed" {
		t.Fatalf("a forged report waited for the provider: %s %q", got.State, got.Note)
	}
	i.source.outage.Store(0)
	c = i.refresh(t)
	asked := i.source.asked.Load()
	i.source.outage.Store(503)
	i.runner.reportImport(c, latest, importHonest)
	if got := i.operation(t, c.Id); got.State != "succeeded" {
		t.Fatalf("an honest report waited for the provider: %s %q", got.State, got.Note)
	}
	if i.source.asked.Load() != asked {
		t.Fatal("a report asked the provider")
	}
}

// An owner's refresh hears at once, with nothing started. A push's refresh retries by itself
// and shows the commit it is waiting to import.
func TestImportAdmissionWaitsForAProviderOutage(t *testing.T) {
	i := newImportedEnvironment(t, "gitea")
	latest := strings.Repeat("e", 40)
	i.source.head.Store(latest)
	i.source.tree.record(latest, importHonest)
	for _, status := range []int64{500, 503, 429, 403, 401} {
		i.source.outage.Store(status)
		i.b.call("POST", i.path+"/source/refresh", map[string]any{"baseRevisionId": i.draft.Id}, 503, nil)
		if count := i.importOperations(t, latest); count != 0 {
			t.Fatalf("HTTP %d: %d imports started that could not be checked", status, count)
		}
	}
	i.source.outage.Store(503)
	i.source.push(latest)
	waitFor(t, "the pending commit", 15*time.Second, func() bool { return i.pendingCommit(t, i.path) == latest })
	if count := i.importOperations(t, latest); count != 0 {
		t.Fatalf("%d imports started that could not be checked", count)
	}
	i.source.outage.Store(0)
	i.retryRefresh(t)
	c := i.runner.next()
	if pending := i.pendingCommit(t, i.path); pending != "" {
		t.Fatal("a started import stayed pending", pending)
	}
	i.runner.reportImport(c, latest, importHonest)
	if got := i.operation(t, c.Id); got.State != "succeeded" {
		t.Fatalf("the retried import did not finish: %s %q", got.State, got.Note)
	}
}

// addSource binds another environment on the same connection and runner, and imports head.
func (i *importedEnvironment) addSource(t *testing.T, name string, paths []string) (string, uuid.UUID) {
	t.Helper()
	var project api.Project
	i.b.call("POST", i.prefix+"/projects", map[string]string{"name": name}, 201, &project)
	var env api.Environment
	i.b.call("POST", i.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	path := i.prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var op api.Operation
	i.b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: paths, Entrypoints: paths[:1]}}, 202, &op)
	files := []composeproject.File{}
	for _, name := range paths {
		files = append(files, composeproject.File{Path: name, Content: importHonest[0].Content[:strings.Index(importHonest[0].Content, "    env_file")]})
	}
	c := i.runner.next()
	if c.Id != op.Id {
		t.Fatal("the added source's import did not reach the runner")
	}
	i.runner.reportImport(c, i.source.head.Load().(string), files)
	return path, env.Id
}

// A file one source cannot read must not hold back the other sources on that branch.
func TestRefreshStartsOtherSourcesWhenOneCannotBeRead(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	other := composeproject.File{Path: "other.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"}
	all := append(append([]composeproject.File{}, importHonest...), other)
	head := i.source.head.Load().(string)
	i.source.tree.record(head, all)
	otherPath, _ := i.addSource(t, "Other", []string{"other.yaml"})

	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	i.source.failing.Store("other.yaml")
	i.source.outage.Store(500)
	i.source.push(latest)
	c := i.runner.next()
	// A failure on the runner must not be repeated by the other source's retries.
	i.runner.finishPipeline(c, "failed", pipeline.Result{})
	waitFor(t, "the pending commit", 15*time.Second, func() bool { return i.pendingCommit(t, otherPath) == latest })
	i.retryRefresh(t)
	if pending := i.pendingCommit(t, otherPath); pending != latest {
		t.Fatal("the retry forgot the waiting commit", pending)
	}
	if count := i.importOperations(t, latest); count != 1 {
		t.Fatalf("%d imports started with one source unreadable", count)
	}
	i.source.outage.Store(0)
	i.retryRefresh(t)
	c = i.runner.next()
	i.runner.reportImport(c, latest, []composeproject.File{other})
	var op api.Operation
	i.b.call("GET", otherPath+"/operations/"+c.Id.String(), nil, 200, &op)
	if op.State != "succeeded" {
		t.Fatalf("the retried source did not import: %s %q", op.State, op.Note)
	}
}

// Sources share one job's limited passes through the provider's answers, however many there are.
func TestRefreshAsksAboutEverySourceTogether(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	files := append([]composeproject.File{}, importHonest...)
	selections := [][]string{{"shared.yaml"}, {"shared.yaml"}}
	for n := range 8 {
		selections = append(selections, []string{fmt.Sprintf("compose-%d.yaml", n)})
	}
	for _, selection := range selections {
		files = append(files, composeproject.File{Path: selection[0], Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"})
	}
	head := i.source.head.Load().(string)
	i.source.tree.record(head, files)
	for n, selection := range selections {
		i.addSource(t, fmt.Sprintf("Source %d", n), selection)
	}
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, files)
	asked := i.source.asked.Load()
	i.source.push(latest)
	waitFor(t, "every source's import", 15*time.Second, func() bool { return i.importOperations(t, latest) == len(selections)+1 })
	// The two sources selecting the same file ask about it once.
	if got, want := i.source.asked.Load()-asked, int64(len(importPaths)+len(selections)-1); got != want {
		t.Fatalf("asked %d questions, want %d", got, want)
	}
}

// A provider that stops answering about one source costs that source, not the others: running
// out of time is recorded, and the next attempt asks about the others first.
func TestRefreshRecordsAProviderThatNeverAnswers(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	other := composeproject.File{Path: "other.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"}
	all := append(append([]composeproject.File{}, importHonest...), other)
	head := i.source.head.Load().(string)
	i.source.tree.record(head, all)
	otherPath, otherEnv := i.addSource(t, "Other", []string{"other.yaml"})
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	// Sources are asked about in order of environment, so hold the first one's file.
	held, heldPath, freeFile, freePath := "compose.yaml", i.path, []composeproject.File{other}, otherPath
	if strings.Compare(otherEnv.String(), i.env.Id.String()) < 0 {
		held, heldPath, freeFile, freePath = "other.yaml", otherPath, importHonest, i.path
	}
	i.source.holdPath(t, held)
	i.source.push(latest)
	waitFor(t, "the held source's commit", 15*time.Second, func() bool { return i.pendingCommit(t, heldPath) == latest })
	i.retryRefresh(t)
	c := i.runner.next()
	i.runner.reportImport(c, latest, freeFile)
	var op api.Operation
	i.b.call("GET", freePath+"/operations/"+c.Id.String(), nil, 200, &op)
	if op.State != "succeeded" {
		t.Fatalf("the source behind the held one did not import: %s %q", op.State, op.Note)
	}
}

// A build uses the draft its source imports, so it cannot start ahead of that import.
func TestRefreshBuildsAfterItsEnvironmentsImport(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	_, spec := draftSpec(t, i.b, i.path)
	cfg := pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
	i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	i.runner.finishPipeline(i.runner.next(), "cancelled", pipeline.Result{})

	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, importHonest)
	i.source.outage.Store(503)
	i.source.push(latest)
	waitFor(t, "the pending commit", 15*time.Second, func() bool { return i.pendingCommit(t, i.path) == latest })
	var started int
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND kind='pipeline' AND command->'pipeline'->>'commit'=$2", i.ws, latest).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatal("a build started ahead of its environment's import")
	}
	i.source.outage.Store(0)
	i.retryRefresh(t)
	c := i.runner.next()
	var command pipeline.Command
	if err := json.Unmarshal(*c.Pipeline, &command); err != nil || command.Action != "import" {
		t.Fatal("the import did not start first", command.Action, err)
	}
	i.runner.reportImport(c, latest, importHonest)
	draft, _ := draftSpec(t, i.b, i.path)
	waitFor(t, "the build", 20*time.Second, func() bool {
		var count int
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND kind='pipeline' AND command->'pipeline'->>'action'='build' AND command->'pipeline'->>'commit'=$2 AND command->'pipeline'->>'revisionId'=$3", i.ws, latest, draft.Id.String()).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count == 1
	})
}

// A branch moved back to a commit this job already imported must import it again, or the
// source stays on what the owner just rolled back.
func TestRefreshImportsABranchMovedBack(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	other := composeproject.File{Path: "other.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"}
	all := append(append([]composeproject.File{}, importHonest...), other)
	i.source.tree.record(i.source.head.Load().(string), all)
	i.addSource(t, "Other", []string{"other.yaml"})
	good, bad := strings.Repeat("e", 40), strings.Repeat("f", 40)
	i.source.tree.record(good, all)
	i.source.tree.record(bad, all)
	// The other source keeps this one job alive across all three commits.
	i.source.failing.Store("other.yaml")
	i.source.outage.Store(500)
	i.source.push(good)
	i.runner.reportImport(i.runner.next(), good, importHonest)
	i.source.head.Store(bad)
	i.retryRefresh(t)
	i.runner.reportImport(i.runner.next(), bad, importHonest)
	i.source.head.Store(good)
	i.retryRefresh(t)
	c := i.runner.next()
	var command pipeline.Command
	if err := json.Unmarshal(*c.Pipeline, &command); err != nil || command.Action != "import" || command.Commit != good {
		t.Fatal("the rollback did not import", command.Action, command.Commit, err)
	}
}

// A commit the branch left is no longer waiting to be imported.
func TestRefreshForgetsACommitTheBranchLeft(t *testing.T) {
	i := newImportedEnvironment(t, "gitea")
	imported, latest := i.source.head.Load().(string), strings.Repeat("e", 40)
	i.source.outage.Store(503)
	i.source.push(latest)
	waitFor(t, "the pending commit", 15*time.Second, func() bool { return i.pendingCommit(t, i.path) == latest })
	i.source.head.Store(imported)
	i.retryRefresh(t)
	if pending := i.pendingCommit(t, i.path); pending != "" {
		t.Fatal("a commit the branch left stayed pending", pending)
	}
}

// A source's refresh gives up in time for the next push to start a new one.
func TestRefreshGivesUpOnAnUnreadableSource(t *testing.T) {
	i := newImportedEnvironment(t, "gitea")
	latest := strings.Repeat("e", 40)
	i.source.outage.Store(503)
	i.source.push(latest)
	waitFor(t, "the pending commit", 15*time.Second, func() bool { return i.pendingCommit(t, i.path) == latest })
	if _, err := i.f.st.Pool.Exec(t.Context(), "UPDATE river_job SET created_at=now()-interval '25 hours' WHERE kind='platform_git_refresh_v1'"); err != nil {
		t.Fatal(err)
	}
	i.retryRefresh(t)
	var state string
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT state FROM river_job WHERE kind='platform_git_refresh_v1' ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatal("the refresh kept waiting", state)
	}
	if pending := i.pendingCommit(t, i.path); pending != latest {
		t.Fatal("giving up hid the waiting commit", pending)
	}
}

// Every look at an unreadable source costs provider requests and a shared worker, so another
// source being busy must not make the job look again every few seconds.
func TestRefreshKeepsItsWaitWhileAnotherSourceIsBusy(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	other := composeproject.File{Path: "other.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"}
	all := append(append([]composeproject.File{}, importHonest...), other)
	i.source.tree.record(i.source.head.Load().(string), all)
	busyPath, _ := i.addSource(t, "Busy", []string{"other.yaml"})
	draft, _ := draftSpec(t, i.b, busyPath)
	// Left unreported, this owner's refresh keeps its environment busy.
	i.b.call("POST", busyPath+"/source/refresh", map[string]any{"baseRevisionId": draft.Id}, 202, nil)
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	i.source.failing.Store("compose.yaml")
	i.source.outage.Store(500)
	i.source.push(latest)
	waitFor(t, "the pending commit", 15*time.Second, func() bool { return i.pendingCommit(t, i.path) == latest })
	waitFor(t, "the job to wait", 15*time.Second, func() bool {
		var state string
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT state FROM river_job WHERE kind='platform_git_refresh_v1' ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state != "running"
	})
	var wait time.Duration
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT scheduled_at-now() FROM river_job WHERE kind='platform_git_refresh_v1' ORDER BY id DESC LIMIT 1").Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait < 30*time.Second {
		t.Fatalf("the job looks again in %s", wait)
	}
}

// A source the job's last attempt never reached must show it is waiting when the job gives up.
// Otherwise it stays on its old commit with nothing to say so.
func TestRefreshShowsEverySourceWhenItGivesUp(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	other := composeproject.File{Path: "other.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"}
	all := append(append([]composeproject.File{}, importHonest...), other)
	i.source.tree.record(i.source.head.Load().(string), all)
	otherPath, otherEnv := i.addSource(t, "Other", []string{"other.yaml"})
	first, second := strings.Repeat("e", 40), strings.Repeat("f", 40)
	i.source.tree.record(first, all)
	i.source.tree.record(second, all)
	// Sources are asked about in order of environment, so hold the first one's file.
	held, freePath := "compose.yaml", otherPath
	if otherEnv.String() < i.env.Id.String() {
		held, freePath = "other.yaml", i.path
	}
	i.source.holdPath(t, held)
	i.source.push(first)
	waitFor(t, "the job to wait", 15*time.Second, func() bool {
		var waiting int
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind='platform_git_refresh_v1' AND state='scheduled'").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting == 1
	})
	// The job is past its limit and the branch moved, so neither source is shown waiting yet.
	if _, err := i.f.st.Pool.Exec(t.Context(), "UPDATE river_job SET created_at=now()-interval '25 hours' WHERE kind='platform_git_refresh_v1'"); err != nil {
		t.Fatal(err)
	}
	i.source.head.Store(second)
	i.retryRefresh(t)
	if pending := i.pendingCommit(t, freePath); pending != second {
		t.Fatalf("the unreached source was dropped silently: pending %q", pending)
	}
	var open int
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind='platform_git_refresh_v1' AND finalized_at IS NULL").Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatal("the job did not give up")
	}
}

// Past its limit the job must stop reading a source, even while other work keeps it alive.
func TestRefreshStopsReadingAtItsLimit(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	other := composeproject.File{Path: "other.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"}
	all := append(append([]composeproject.File{}, importHonest...), other)
	i.source.tree.record(i.source.head.Load().(string), all)
	busyPath, _ := i.addSource(t, "Busy", []string{"other.yaml"})
	draft, _ := draftSpec(t, i.b, busyPath)
	// Left unreported, this owner's refresh keeps its environment busy.
	i.b.call("POST", busyPath+"/source/refresh", map[string]any{"baseRevisionId": draft.Id}, 202, nil)
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	i.source.failing.Store("compose.yaml")
	i.source.outage.Store(500)
	i.source.push(latest)
	waitFor(t, "the pending commit", 15*time.Second, func() bool { return i.pendingCommit(t, i.path) == latest })
	if _, err := i.f.st.Pool.Exec(t.Context(), "UPDATE river_job SET created_at=now()-interval '25 hours' WHERE kind='platform_git_refresh_v1'"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first attempt to end", 15*time.Second, func() bool {
		var waiting int
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind='platform_git_refresh_v1' AND state='scheduled'").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting == 1
	})
	asked := i.source.asked.Load()
	i.retryRefresh(t)
	if got := i.source.asked.Load(); got != asked {
		t.Fatalf("read files %d more times past the limit", got-asked)
	}
}

// The provider is a tenant's own server and can be slow for as long as its client allows.
func TestImportAdmissionHoldsNothingDuringTheProviderCall(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(i *importedEnvironment, latest string)
	}{
		{"an owner's refresh", func(i *importedEnvironment, latest string) {
			i.source.head.Store(latest)
			i.b.send("POST", i.path+"/source/refresh", map[string]any{"baseRevisionId": i.draft.Id})
		}},
		{"a push", func(i *importedEnvironment, latest string) { i.source.push(latest) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newImportedEnvironment(t, "gitea")
			latest := strings.Repeat("e", 40)
			i.source.tree.record(latest, importHonest)
			entered, release := i.source.hold(t)
			tc.start(i, latest)
			waitEntered(t, entered)
			h := hungBuild{f: i.f, server: i.runnerID, connection: i.source.connection, ws: i.ws, token: i.runner.token, enrolled: i.in}
			h.requireNothingHeld(t, uuid.New())
			release()
			c := i.runner.next()
			i.runner.reportImport(c, latest, importHonest)
			if got := i.operation(t, c.Id); got.State != "succeeded" {
				t.Fatalf("the import did not finish: %s %q", got.State, got.Note)
			}
		})
	}
}

// An App's repository answers through a token minted for reading.
func TestImportCheckThroughAGitHubApp(t *testing.T) {
	h := newHeldGitHub(t)
	f, b := h.f, h.b
	connection := h.connect(t)
	var project api.Project
	b.call("POST", h.prefix+"/projects", map[string]string{"name": "App"}, 201, &project)
	var env api.Environment
	b.call("POST", h.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	path := h.prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	yes := true
	enrolled, in, token := enrollment(t, f, b, h.ws, "build")
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	runner := authorityAgent{t, f, token, in}
	head := strings.Repeat("a", 40)
	h.tree.record(head, importHonest)
	var op api.Operation
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: connection, Branch: "main", RunnerID: enrolled.Server.Id, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, &op)
	runner.reportImport(runner.next(), head, withForgedService(importHonest))
	draft, _ := draftSpec(t, b, path)
	if draft.Id != env.DraftRevisionId {
		t.Fatal("a forged App import wrote a revision")
	}
	b.call("POST", path+"/source/refresh", map[string]any{"baseRevisionId": env.DraftRevisionId}, 202, &op)
	runner.reportImport(runner.next(), head, importHonest)
	if draft, _ = draftSpec(t, b, path); draft.Id == env.DraftRevisionId {
		t.Fatal("an honest App import was not saved")
	}
	if got := h.fileToken.Load(); got != "Bearer installation-read-canary" {
		t.Fatalf("the check used another credential: %v", got)
	}
}
