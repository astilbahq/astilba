package integration

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/google/uuid"
)

// addRunner enrolls another build-only runner in the environment's workspace.
func (i *importedEnvironment) addRunner(t *testing.T) (uuid.UUID, authorityAgent) {
	t.Helper()
	yes := true
	enrolled, in, token := enrollment(t, i.f, i.b, i.ws, "build")
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, i.f.server.Client(), i.f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	return enrolled.Server.Id, authorityAgent{t, i.f, token, in}
}

// addSourceOn is addSource with the source's imports on runner.
func (i *importedEnvironment) addSourceOn(t *testing.T, name string, file composeproject.File, runnerID uuid.UUID, runner authorityAgent) string {
	t.Helper()
	return i.addSourceWithFallback(t, name, file, runnerID, runner, nil)
}

func (i *importedEnvironment) addSourceWithFallback(t *testing.T, name string, file composeproject.File, runnerID uuid.UUID, runner authorityAgent, fallback *uuid.UUID) string {
	t.Helper()
	var project api.Project
	i.b.call("POST", i.prefix+"/projects", map[string]string{"name": name}, 201, &project)
	var env api.Environment
	i.b.call("POST", i.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	path := i.prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var op api.Operation
	i.b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: runnerID, Paths: []string{file.Path}, Entrypoints: []string{file.Path}, FallbackRunnerID: fallback}}, 202, &op)
	c := runner.next()
	if c.Id != op.Id {
		t.Fatal("the added source's import did not reach its runner")
	}
	runner.reportImport(c, i.source.head.Load().(string), []composeproject.File{file})
	return path
}

func (i *importedEnvironment) sourceStatus(t *testing.T, path string) api.GitSourceStatus {
	t.Helper()
	var status api.GitSourceStatus
	i.b.call("GET", path+"/source", nil, 200, &status)
	return status
}

// refreshEnded waits for the branch's newest refresh job to finish, rather than retry or wait.
func (i *importedEnvironment) refreshEnded(t *testing.T) {
	t.Helper()
	waitFor(t, "the refresh to end", 20*time.Second, func() bool {
		var state string
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT state FROM river_job WHERE kind='platform_git_refresh_v1' ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state == "completed"
	})
}

func (i *importedEnvironment) succeeded(t *testing.T, path string, c api.ExecutionCommand) {
	t.Helper()
	var op api.Operation
	i.b.call("GET", path+"/operations/"+c.Id.String(), nil, 200, &op)
	if op.State != "succeeded" {
		t.Fatalf("the import did not succeed: %s %q", op.State, op.Note)
	}
}

func importCommand(t *testing.T, c api.ExecutionCommand) pipeline.Command {
	t.Helper()
	var command pipeline.Command
	if err := json.Unmarshal(*c.Pipeline, &command); err != nil {
		t.Fatal(err)
	}
	return command
}

var otherSourceFile = composeproject.File{Path: "other.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n"}

// A file removed upstream from one source's selection must not stop another source on the branch
// from importing, and the broken source says which file is gone instead of failing on its runner.
func TestRefreshImportsOtherSourcesWhenOnesFileIsGone(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	head := i.source.head.Load().(string)
	i.source.tree.record(head, append(append([]composeproject.File{}, importHonest...), otherSourceFile))
	otherPath := i.addSourceOn(t, "Other", otherSourceFile, i.runnerID, i.runner)

	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, importHonest)
	i.source.push(latest)
	c := i.runner.next()
	if command := importCommand(t, c); command.Commit != latest || command.Source.Paths[0] != importPaths[0] {
		t.Fatal("the other source's import did not start", command.Commit, command.Source.Paths)
	}
	i.runner.reportImport(c, latest, importHonest)
	i.succeeded(t, i.path, c)
	// The failure is recorded, and nothing waits on it.
	i.refreshEnded(t)
	failure := i.sourceStatus(t, otherPath).Failure
	if failure == nil || failure.Commit != latest || !strings.Contains(failure.Reason, `"other.yaml"`) {
		t.Fatalf("the broken source does not say why: %+v", failure)
	}
	if count := i.importOperations(t, latest); count != 1 {
		t.Fatalf("%d imports started for the commit, want only the readable source's", count)
	}
	if i.sourceStatus(t, i.path).Failure != nil {
		t.Fatal("the source that imported shows a failure")
	}
}

// A revoked runner is a configuration only an owner can change. Its source and build target say
// so, while the branch's other source imports.
func TestRefreshImportsOtherSourcesWhenOnesRunnerIsRevoked(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	head := i.source.head.Load().(string)
	all := append(append([]composeproject.File{}, importHonest...), otherSourceFile)
	i.source.tree.record(head, all)
	revokedID, revoked := i.addRunner(t)
	otherPath := i.addSourceOn(t, "Other", otherSourceFile, revokedID, revoked)
	_, spec := draftSpec(t, i.b, i.path)
	app := spec.Applications[0].ID
	cfg := pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: revokedID, Context: ".", Dockerfile: "Dockerfile", Architecture: string(revoked.in.Capabilities.Arch), Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
	i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	revoked.finishPipeline(revoked.next(), "cancelled", pipeline.Result{})
	i.b.call("POST", i.prefix+"/servers/"+revokedID.String()+"/revoke", nil, 204, nil)

	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	i.source.push(latest)
	c := i.runner.next()
	i.runner.reportImport(c, latest, importHonest)
	i.succeeded(t, i.path, c)
	failure := i.sourceStatus(t, otherPath).Failure
	if failure == nil || failure.Commit != latest || !strings.Contains(failure.Reason, "runner") {
		t.Fatalf("the source on the revoked runner does not say why: %+v", failure)
	}
	// The build waited for its environment's import, then recorded why it can't start.
	waitFor(t, "the target's rejection", 20*time.Second, func() bool {
		var rejections []api.BuildRejection
		i.b.call("GET", i.path+"/build-rejections", nil, 200, &rejections)
		return len(rejections) == 1 && rejections[0].ApplicationId == app && rejections[0].Commit == latest && rejections[0].Reason == api.RunnerUnavailable
	})
	i.refreshEnded(t)
}

// A selected file renamed upstream breaks every refresh until an owner binds the source again with
// its new name, which imports it.
func TestRenamedSourceFileImportsAfterRebinding(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	renamed := []composeproject.File{{Path: "docker-compose.yaml", Content: importHonest[0].Content}, importHonest[1]}
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, renamed)
	i.source.push(latest)
	i.refreshEnded(t)
	failure := i.sourceStatus(t, i.path).Failure
	if failure == nil || failure.Commit != latest || !strings.Contains(failure.Reason, `"compose.yaml"`) {
		t.Fatalf("the source does not name the missing file: %+v", failure)
	}
	if count := i.importOperations(t, latest); count != 0 {
		t.Fatal("an import of a commit without the selected file started")
	}
	// A refresh started by hand says the same straight away.
	var refused api.Error
	if err := json.Unmarshal(i.b.call("POST", i.path+"/source/refresh", map[string]any{"baseRevisionId": i.draft.Id}, 400, nil), &refused); err != nil || !strings.Contains(refused.Message, `"compose.yaml"`) {
		t.Fatalf("the refresh's refusal does not name the file: %q %v", refused.Message, err)
	}

	source := pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: []string{"docker-compose.yml", "config/app.env"}, Entrypoints: []string{"docker-compose.yml"}}
	shown := i.sourceStatus(t, i.path).SourceHash
	// Binding again names the source as it was shown, so a change since is refused, not overwritten.
	i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": source}, 409, nil)
	i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": source, "sourceHash": strings.Repeat("0", 64)}, 409, nil)
	// A misspelt new name is refused by name, and changes nothing.
	if err := json.Unmarshal(i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": source, "sourceHash": shown}, 400, nil), &refused); err != nil || !strings.Contains(refused.Message, `"docker-compose.yml"`) {
		t.Fatalf("binding to a missing file was not refused by name: %q %v", refused.Message, err)
	}
	if status := i.sourceStatus(t, i.path); status.Failure == nil || status.ImportedCommit != strings.Repeat("a", 40) {
		t.Fatal("a refused binding changed the source")
	}

	source.Paths, source.Entrypoints = []string{"docker-compose.yaml", "config/app.env"}, []string{"docker-compose.yaml"}
	var op api.Operation
	i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": source, "sourceHash": shown}, 202, &op)
	if status := i.sourceStatus(t, i.path); status.Failure != nil || status.ImportedRevisionId != nil {
		t.Fatal("binding again kept the failure or the earlier import", status.Failure, status.ImportedRevisionId)
	}
	// Binding again made the source the owner's, which the audit record says; the refused attempt left none.
	if rebinds, binds := auditedBy(t, i.f, i.ws, i.f.owner, "git.source.rebind"), auditedBy(t, i.f, i.ws, i.f.owner, "git.source.bind"); rebinds != 1 || binds != 1 {
		t.Fatalf("audited %d rebinds and %d binds, want one of each", rebinds, binds)
	}
	c := i.runner.next()
	if c.Id != op.Id || importCommand(t, c).Commit != latest {
		t.Fatal("binding again did not import the branch's head")
	}
	i.runner.reportImport(c, latest, renamed)
	i.succeeded(t, i.path, c)
	status := i.sourceStatus(t, i.path)
	if status.ImportedCommit != latest || status.Status != "ready" || status.Configuration.Paths[0] != "docker-compose.yaml" || !status.Clean {
		t.Fatalf("the source did not import its new files: %+v", status)
	}
	_, spec := draftSpec(t, i.b, i.path)
	if spec.Compose.Entrypoints[0] != "docker-compose.yaml" {
		t.Fatal("the draft does not hold the renamed file", spec.Compose.Entrypoints)
	}

	// Later pushes import the new selection.
	next := strings.Repeat("f", 40)
	i.source.tree.record(next, renamed)
	i.source.push(next)
	c = i.runner.next()
	if command := importCommand(t, c); command.Commit != next || command.Source.Entrypoints[0] != "docker-compose.yaml" {
		t.Fatal("the next push did not import the new selection", command.Source.Entrypoints)
	}
}

// An environment runs one import at a time, so binding again waits for one that is running. An
// owner can unbind the source while it imports, and the import then saves nothing.
func TestUnbindingDuringAnImportSavesNothing(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, importHonest)
	i.source.head.Store(latest)
	running := i.refresh(t)
	shown := i.sourceStatus(t, i.path).SourceHash
	var refused api.Error
	if err := json.Unmarshal(i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}, "sourceHash": shown}, 409, nil), &refused); err != nil || refused.Fields == nil || (*refused.Fields)[0] != "environment.operation" {
		t.Fatalf("binding again during an import was not refused for it: %+v %v", refused, err)
	}
	i.b.call("DELETE", i.path+"/source", nil, 204, nil)
	i.runner.reportImport(running, latest, importHonest)
	if unbound := i.operation(t, running.Id); unbound.State != "failed" || !strings.Contains(unbound.Note, "unbound") {
		t.Fatalf("an import for an unbound source was accepted: %s %q", unbound.State, unbound.Note)
	}
	i.b.call("GET", i.path+"/source", nil, 404, nil)
	if after, spec := draftSpec(t, i.b, i.path); after.Id != i.draft.Id || spec.Compose == nil {
		t.Fatal("unbinding changed the draft")
	}
	// A push for the branch finds nothing to import.
	next := strings.Repeat("f", 40)
	i.source.tree.record(next, importHonest)
	i.source.push(next)
	if count := i.importOperations(t, next); count != 0 {
		t.Fatal("an unbound source imported a push")
	}
	// A form opened before the source was removed can't bind it back unseen.
	i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}, "sourceHash": shown}, 409, nil)
	// Binding it again imports the branch.
	var op api.Operation
	i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, &op)
	c := i.runner.next()
	i.runner.reportImport(c, next, importHonest)
	i.succeeded(t, i.path, c)
}

// Binding a source again, or unbinding it, changes whose approval automatic work runs on and what it
// imports, so it takes an owner.
func TestOnlyAnOwnerRebindsOrUnbindsASource(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	i.b.call("POST", i.prefix+"/invitations", map[string]string{"email": "operator@example.test", "role": "operator"}, 201, nil)
	operator := i.f.browser(t)
	var accepted api.Session
	operator.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, i.f, "/invitation"), "name": "Operator", "password": i.f.password}, 200, &accepted)
	operator.csrf = accepted.CsrfToken
	operator.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}, "sourceHash": i.sourceStatus(t, i.path).SourceHash}, 403, nil)
	operator.call("DELETE", i.path+"/source", nil, 403, nil)
	if status := i.sourceStatus(t, i.path); status.ImportedCommit != strings.Repeat("a", 40) {
		t.Fatal("an operator changed the source")
	}
}

// A runner enrolling again has no credential for a while, but it is still the workspace's: its
// source waits for it, shown as not yet imported, rather than failing.
func TestRefreshWaitsForARunnerEnrollingAgain(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	head := i.source.head.Load().(string)
	all := append(append([]composeproject.File{}, importHonest...), otherSourceFile)
	i.source.tree.record(head, all)
	enrollingID, enrolling := i.addRunner(t)
	otherPath := i.addSourceOn(t, "Other", otherSourceFile, enrollingID, enrolling)
	var next api.Enrollment
	i.b.call("POST", i.prefix+"/servers/"+enrollingID.String()+"/enrollment", nil, 201, &next)

	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	i.source.push(latest)
	c := i.runner.next()
	i.runner.reportImport(c, latest, importHonest)
	waitFor(t, "the other source to wait", 15*time.Second, func() bool { return i.pendingCommit(t, otherPath) == latest })
	if failure := i.sourceStatus(t, otherPath).Failure; failure != nil {
		t.Fatalf("a runner enrolling again failed its source: %+v", failure)
	}

	in := enrolling.in
	credential, err := randtoken.New()
	if err != nil {
		t.Fatal(err)
	}
	in.Token, in.Credential = next.Token, &credential
	agentRequest(t, i.f.server.Client(), i.f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	enrolled := authorityAgent{t, i.f, enrollingID.String() + "." + credential, in}
	i.retryRefresh(t)
	c = enrolled.next()
	if command := importCommand(t, c); command.Commit != latest || command.Source.Paths[0] != otherSourceFile.Path {
		t.Fatal("the waiting source did not import once its runner enrolled", command.Commit, command.Source.Paths)
	}
}

// Builds wait for their environment's import, so once binding the source again imports the commit
// a failed refresh held them at, they build it without another push.
func TestBuildsHeldByAFailedImportRunAfterRebinding(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	_, spec := draftSpec(t, i.b, i.path)
	cfg := pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
	i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	i.runner.finishPipeline(i.runner.next(), "cancelled", pipeline.Result{})

	renamed := []composeproject.File{{Path: "docker-compose.yaml", Content: importHonest[0].Content}, importHonest[1]}
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, renamed)
	i.source.push(latest)
	i.refreshEnded(t)
	builds := func() int {
		var count int
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND kind='pipeline' AND command->'pipeline'->>'action'='build' AND command->'pipeline'->>'commit'=$2", i.ws, latest).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if builds() != 0 {
		t.Fatal("a build started ahead of its environment's failed import")
	}

	var op api.Operation
	i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "sourceHash": i.sourceStatus(t, i.path).SourceHash, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: []string{"docker-compose.yaml", "config/app.env"}, Entrypoints: []string{"docker-compose.yaml"}}}, 202, &op)
	c := i.runner.next()
	i.runner.reportImport(c, latest, renamed)
	i.succeeded(t, i.path, c)
	waitFor(t, "the held build", 20*time.Second, func() bool { return builds() == 1 })
}

// A runner whose credential was withdrawn with no enrollment under way won't come back by itself,
// so its source fails and says why rather than waiting.
func TestRefreshFailsASourceWhoseRunnerIsNotEnrolling(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	head := i.source.head.Load().(string)
	all := append(append([]composeproject.File{}, importHonest...), otherSourceFile)
	i.source.tree.record(head, all)
	fencedID, fenced := i.addRunner(t)
	otherPath := i.addSourceOn(t, "Other", otherSourceFile, fencedID, fenced)
	// As an identity conflict fences a server: the credential goes, and nothing is enrolling.
	if _, err := i.f.st.Pool.Exec(t.Context(), "UPDATE servers SET credential_hash=NULL,previous_hash=NULL WHERE id=$1", fencedID); err != nil {
		t.Fatal(err)
	}
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	i.source.push(latest)
	c := i.runner.next()
	i.runner.reportImport(c, latest, importHonest)
	i.refreshEnded(t)
	if failure := i.sourceStatus(t, otherPath).Failure; failure == nil || !strings.Contains(failure.Reason, "runner") {
		t.Fatalf("a runner with no credential coming did not fail its source: %+v", failure)
	}
}

// Unbinding a source that failed lets the automatic builds it held build the branch's head.
func TestUnbindingAFailedSourceLetsItsBuildsRun(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	_, spec := draftSpec(t, i.b, i.path)
	cfg := pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
	i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	i.runner.finishPipeline(i.runner.next(), "cancelled", pipeline.Result{})
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, []composeproject.File{{Path: "docker-compose.yaml", Content: importHonest[0].Content}, importHonest[1]})
	i.source.push(latest)
	i.refreshEnded(t)
	i.b.call("DELETE", i.path+"/source", nil, 204, nil)
	c := i.runner.next()
	if command := importCommand(t, c); command.Action != "build" || command.Commit != latest {
		t.Fatal("unbinding did not start the held build", command.Action, command.Commit)
	}
}

// A fallback that can't take an import isn't used: the import queues on its busy runner, as it would
// with no fallback, rather than failing only when the runner happens to be busy.
func TestRefreshQueuesOnTheRunnerWhenItsFallbackIsGone(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	head := i.source.head.Load().(string)
	all := append(append([]composeproject.File{}, importHonest...), otherSourceFile)
	i.source.tree.record(head, all)
	goneID, _ := i.addRunner(t)
	otherPath := i.addSourceWithFallback(t, "Other", otherSourceFile, i.runnerID, i.runner, &goneID)
	i.b.call("POST", i.prefix+"/servers/"+goneID.String()+"/revoke", nil, 204, nil)
	// The runner is busy with the first environment's import.
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, all)
	i.source.head.Store(latest)
	i.refresh(t)
	i.source.push(latest)
	waitFor(t, "the other source's import", 15*time.Second, func() bool {
		var queued int
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations o JOIN pipeline_runs r ON r.id=o.id WHERE o.server_id=$1 AND r.configuration->>'commit'=$2 AND r.configuration#>>'{source,paths,0}'=$3", i.runnerID, latest, otherSourceFile.Path).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		return queued == 1
	})
	if failure := i.sourceStatus(t, otherPath).Failure; failure != nil {
		t.Fatalf("a gone fallback failed a source whose runner was only busy: %+v", failure)
	}
}

// A build target whose fallback was revoked after it was saved queues its automatic build on its busy
// runner rather than being rejected, and can't wait on that runner for good: revoking it settles the
// build. A runner already revoked refuses a manual build at once.
func TestBuildQueuesOnItsRunnerWhenTheFallbackIsGone(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	_, spec := draftSpec(t, i.b, i.path)
	goneID, _ := i.addRunner(t)
	cfg := pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, FallbackRunnerID: &goneID, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
	i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	i.runner.finishPipeline(i.runner.next(), "cancelled", pipeline.Result{})
	i.b.call("POST", i.prefix+"/servers/"+goneID.String()+"/revoke", nil, 204, nil)
	// The runner is busy with another environment's import.
	var project api.Project
	i.b.call("POST", i.prefix+"/projects", map[string]string{"name": "Busy"}, 201, &project)
	var env api.Environment
	i.b.call("POST", i.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	i.b.call("PUT", i.prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, nil)
	i.runner.next()

	// The source tracks a branch of its own, so a push to main exercises only the target.
	if _, err := i.f.st.Pool.Exec(t.Context(), `UPDATE git_sources SET configuration=jsonb_set(configuration,'{branch}','"release"') WHERE environment_id=$1`, i.env.Id); err != nil {
		t.Fatal(err)
	}
	latest := strings.Repeat("e", 40)
	i.source.tree.record(latest, importHonest)
	i.source.push(latest)
	var build uuid.UUID
	waitFor(t, "the automatic build", 20*time.Second, func() bool {
		err := i.f.st.Pool.QueryRow(t.Context(), "SELECT id FROM execution_operations WHERE workspace_id=$1 AND kind='pipeline' AND command->'pipeline'->>'action'='build' AND command->'pipeline'->>'commit'=$2", i.ws, latest).Scan(&build)
		return err == nil
	})
	if op := i.operation(t, build); op.ServerId != i.runnerID || op.State != "queued" {
		t.Fatalf("the build did not queue on its runner: server %s, state %s", op.ServerId, op.State)
	}
	var rejections []api.BuildRejection
	i.b.call("GET", i.path+"/build-rejections", nil, 200, &rejections)
	if len(rejections) != 0 {
		t.Fatalf("a gone fallback rejected a target whose runner was only busy: %+v", rejections)
	}
	i.b.call("POST", i.prefix+"/servers/"+i.runnerID.String()+"/revoke", nil, 204, nil)
	if settled := i.operation(t, build); settled.State != "failed" && settled.State != "cancelled" {
		t.Fatalf("a build queued on a revoked runner still waits: %s", settled.State)
	}
	// A runner that is already revoked refuses a manual build rather than queueing it.
	cfg.FallbackRunnerID = nil
	i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": cfg}, 404, nil)
}

// A fallback whose agent can't run builds without root isn't ready for a build, so the build queues
// on its busy runner, as it does when the runner is idle.
func TestManualBuildQueuesOnItsRunnerWhenTheFallbackCantBuild(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	_, spec := draftSpec(t, i.b, i.path)
	yes, no := true, false
	older, in, token := enrollment(t, i.f, i.b, i.ws, "build")
	in.Capabilities.Build, in.Capabilities.BuildNetwork, in.Capabilities.RootlessBuilds = &yes, &yes, &no
	agentRequest(t, i.f.server.Client(), i.f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	// Its heartbeat keeps the older runner online and idle.
	agentRequest(t, i.f.server.Client(), i.f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	var project api.Project
	i.b.call("POST", i.prefix+"/projects", map[string]string{"name": "Busy"}, 201, &project)
	var env api.Environment
	i.b.call("POST", i.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	i.b.call("PUT", i.prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, nil)
	i.runner.next()

	cfg := pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, FallbackRunnerID: &older.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
	var build api.Operation
	i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": cfg}, 202, &build)
	if build.ServerId != i.runnerID {
		t.Fatalf("the build went to a fallback that can't run it: %s", build.ServerId)
	}
}

// A fallback runner is saved with a source or build target only if it is one of the workspace's
// servers and not revoked, since choosing a runner passes over one that can't take work.
func TestSavingRefusesAFallbackRunnerThatIsntTheWorkspaces(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	var other api.Workspace
	i.b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Other"}, 201, &other)
	foreign, _, _ := enrollment(t, i.f, i.b, other.Id, "build")
	revokedID, _ := i.addRunner(t)
	i.b.call("POST", i.prefix+"/servers/"+revokedID.String()+"/revoke", nil, 204, nil)
	refusedFor := func(raw []byte) {
		t.Helper()
		var refused api.Error
		if err := json.Unmarshal(raw, &refused); err != nil || refused.Fields == nil || (*refused.Fields)[0] != "configuration.fallbackRunnerId" {
			t.Fatalf("not refused for its fallback runner: %s %v", raw, err)
		}
	}
	_, spec := draftSpec(t, i.b, i.path)
	for _, fallback := range []uuid.UUID{foreign.Server.Id, revokedID} {
		source := pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, FallbackRunnerID: &fallback, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}
		refusedFor(i.b.call("PUT", i.path+"/source", map[string]any{"baseRevisionId": i.draft.Id, "configuration": source, "sourceHash": i.sourceStatus(t, i.path).SourceHash}, 400, nil))
		build := pipeline.BuildConfig{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, FallbackRunnerID: &fallback, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
		refusedFor(i.b.call("POST", i.path+"/builds", map[string]any{"baseRevisionId": i.draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": build}, 400, nil))
	}
	// A first binding is checked the same way.
	var project api.Project
	i.b.call("POST", i.prefix+"/projects", map[string]string{"name": "New"}, 201, &project)
	var env api.Environment
	i.b.call("POST", i.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	foreignID := foreign.Server.Id
	refusedFor(i.b.call("PUT", i.prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: i.source.connection.ID, Branch: "main", RunnerID: i.runnerID, FallbackRunnerID: &foreignID, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 400, nil))
	if status := i.sourceStatus(t, i.path); status.ImportedCommit != strings.Repeat("a", 40) || status.Configuration.FallbackRunnerId != nil {
		t.Fatal("a refused fallback changed the source")
	}
}
