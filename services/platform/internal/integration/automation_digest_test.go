package integration

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

// recordReviewedDeployment models a reviewed deployment of a revision that resolved to images, for
// tests that start from one without driving it. Automatic deployment compares every service with it.
func recordReviewedDeployment(t *testing.T, f *fixture, ws, env, revision, server uuid.UUID, images map[string]string) {
	t.Helper()
	ctx := t.Context()
	var unit uuid.UUID
	var raw []byte
	if err := f.st.Pool.QueryRow(ctx, "SELECT u.id,r.specification FROM deployment_units u JOIN specification_revisions r ON r.workspace_id=u.workspace_id AND r.environment_id=u.environment_id WHERE u.workspace_id=$1 AND u.environment_id=$2 AND u.primary_unit AND r.id=$3", ws, env, revision).Scan(&unit, &raw); err != nil {
		t.Fatal(err)
	}
	spec, _, err := domain.ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := f.st.Pool.QueryRow(ctx, "UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation", ws, env).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	plan := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: env, UnitID: unit, RevisionID: revision, Recipe: deployment.Recipe, ExpiresAt: time.Now().Add(time.Hour), Specification: spec, Images: images}.WithHash()
	body := deployment.Payload{Action: "deploy", Plan: plan}
	applied := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, ResourceId: unit, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Visual: claimMessage(t, body)}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE deployment_units SET server_id=$3 WHERE workspace_id=$1 AND environment_id=$2", []any{ws, env, server}},
		{"INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,$4,$5,$6,$7,'visual','succeeded',1)", []any{applied.Id, ws, server, env, generation, uuid.New(), claimBytes(t, applied)}},
		{"INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')", []any{plan.ID, ws, server, env, unit, revision, applied.Id}},
		{"INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)", []any{plan.ID, ws, env, claimBytes(t, plan)}},
		{"UPDATE execution_operations SET runtime_outcome='applied',runtime_plan_id=$2 WHERE id=$1", []any{applied.Id, plan.ID}},
		{"INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,'{}')", []any{ws, env, unit, server, plan.ID}},
		{"UPDATE environments SET applied_revision_id=$3,runtime_operation_id=$4 WHERE workspace_id=$1 AND id=$2", []any{ws, env, revision, applied.Id}},
	} {
		if _, err := f.st.Pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

// automaticCompose is a Compose environment that imports from Git and that the owner has reviewed
// and deployed once, with automatic deployment of imported commits turned on.
type automaticCompose struct {
	t      *testing.T
	b      *browser
	agent  authorityAgent
	source *gitSource
	path   string
	server uuid.UUID
	ns     uuid.UUID
	commit int
}

func newAutomaticCompose(t *testing.T, compose string, images func(id func(string) string) map[string]string) (*automaticCompose, map[string]string) {
	t.Helper()
	f := setup(t)
	b := f.browser(t)
	ws, prefix, env, path := composeEnvironment(t, f, b)
	source := newGitSource(t, f, b, prefix)
	enrolled, in, agentToken := enrollment(t, f, b, ws, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.Visual, in.Capabilities.Compose = &yes, &yes, &yes
	in.Capabilities.BuildNetwork = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	a := &automaticCompose{t: t, b: b, agent: authorityAgent{t, f, agentToken, in}, source: source, path: path, server: enrolled.Server.Id, ns: env.DeploymentUnit.Id}
	first := a.nextCommit()
	source.tree.record(first, []composeproject.File{{Path: "compose.yaml", Content: compose}})
	var op api.Operation
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: source.connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}}, 202, &op)
	a.agent.finishPipeline(a.agent.next(), "succeeded", pipeline.Result{Commit: first, Files: []composeproject.File{{Path: "compose.yaml", Content: compose}}})
	draft, _ := draftSpec(t, b, path)
	reviewed := images(a.id)
	a.agent.reviewAndApply(b, path, a.server, draft.Id, reviewed)
	b.call("PUT", path+"/source/policy", map[string]bool{"autoDeploy": true}, 204, nil)
	return a, reviewed
}

func (a *automaticCompose) id(name string) string {
	return composeproject.ResourceID(a.ns, "service", name).String()
}

func (a *automaticCompose) nextCommit() string {
	a.commit++
	return strings.Repeat(string(rune('a'+a.commit-1)), 40)
}

// push imports a commit holding compose.yaml and returns its run.
func (a *automaticCompose) push(compose string) uuid.UUID {
	a.t.Helper()
	commit := a.nextCommit()
	files := []composeproject.File{{Path: "compose.yaml", Content: compose}}
	a.source.tree.record(commit, files)
	a.source.push(commit)
	c := a.agent.next()
	if c.Kind != "pipeline" {
		a.t.Fatal("push did not start an import", c.Kind)
	}
	a.agent.finishPipeline(c, "succeeded", pipeline.Result{Commit: commit, Files: files})
	return c.Id
}

// blockedAfterResolve answers the automatic deployment's image inspection and returns the note
// it stopped with.
func (a *automaticCompose) blockedAfterResolve(run uuid.UUID, images map[string]string) string {
	a.t.Helper()
	c := a.agent.next()
	var payload deployment.Payload
	if c.Kind != "visual" || json.Unmarshal(*c.Visual, &payload) != nil || payload.Action != "resolve" {
		a.t.Fatal("automatic deployment did not start with image review", c.Kind, payload.Action)
	}
	a.agent.observe(c, deployment.Observation{Images: images, StateHash: strings.Repeat("d", 64)})
	return waitBlocked(a.t, a.b, a.path, run)
}

func digest(c byte) string { return "@sha256:" + strings.Repeat(string(c), 64) }

func with(images map[string]string, id, image string) map[string]string {
	out := map[string]string{}
	for k, v := range images {
		out[k] = v
	}
	out[id] = image
	return out
}

// A privileged service's approval covers exactly what it runs, so only an identical definition at
// the same digest deploys automatically.
func TestAutomationStopsForChangedPrivilegedService(t *testing.T) {
	compose := "services:\n  agent:\n    image: monitor:latest\n    privileged: true\n    command: [run]\n    environment:\n      LEVEL: info\n  web:\n    image: " + pipelineTestImage + "\n"
	a, reviewed := newAutomaticCompose(t, compose, func(id func(string) string) map[string]string {
		return map[string]string{id("agent"): "monitor" + digest('1'), id("web"): pipelineTestImage}
	})

	a.push("# unchanged definition\n" + compose)
	if plan := a.agent.expectAutomaticDeploy(reviewed); !plan.HostChanges {
		t.Fatal("an identical privileged service did not carry its approval forward")
	}
	for _, change := range []struct{ name, old, replacement string }{
		{"command", "command: [run]", "command: [run, --shell]"},
		{"environment", "LEVEL: info", "LEVEL: debug"},
		{"image", "monitor:latest", "monitor:2"},
	} {
		compose = strings.Replace(compose, change.old, change.replacement, 1)
		note := waitBlocked(t, a.b, a.path, a.push(compose))
		if !strings.Contains(note, "agent runs privileged or with added Linux capabilities") {
			t.Fatalf("changed %s: note does not explain the privileged review: %q", change.name, note)
		}
		expectReviewNote(t, note)
		draft, spec := draftSpec(t, a.b, a.path)
		if !strings.Contains(spec.Compose.Files[0].Content, change.replacement) {
			t.Fatalf("changed %s: the blocked import was not saved for review", change.name)
		}
		a.agent.reviewAndApply(a.b, a.path, a.server, draft.Id, reviewed)
	}

	run := a.push("# same definition, moved tag\n" + compose)
	note := a.blockedAfterResolve(run, with(reviewed, a.id("agent"), "monitor"+digest('2')))
	if !strings.Contains(note, "agent runs privileged") || !strings.Contains(note, "different digest") {
		t.Fatalf("note does not explain the privileged digest: %q", note)
	}
}

// Every service this run did not change keeps the image the last reviewed deployment ran.
func TestAutomationImportStopsForMovedTagOnAnotherService(t *testing.T) {
	compose := "services:\n  web:\n    image: " + pipelineTestImage + "\n  cache:\n    image: redis:latest\n"
	a, reviewed := newAutomaticCompose(t, compose, func(id func(string) string) map[string]string {
		return map[string]string{id("web"): pipelineTestImage, id("cache"): "redis" + digest('1')}
	})

	// The commit's own image edit deploys.
	edited := "busybox" + digest('2')
	compose = strings.Replace(compose, pipelineTestImage, edited, 1)
	a.push(compose)
	reviewed = with(reviewed, a.id("web"), edited)
	if plan := a.agent.expectAutomaticDeploy(reviewed); plan.Images[a.id("web")] != edited {
		t.Fatal("the commit's own image did not deploy")
	}

	run := a.push("# unrelated change\n" + compose)
	note := a.blockedAfterResolve(run, with(reviewed, a.id("cache"), "redis"+digest('3')))
	if !strings.Contains(note, "image tag for cache") || !strings.Contains(note, "pin the image by digest") {
		t.Fatalf("note does not explain the moved tag: %q", note)
	}
	expectReviewNote(t, note)
}

// A build's own image deploys, but not alongside another service's moved tag.
func TestAutomationBuildStopsForMovedTagOnAnotherService(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws, prefix, env, path := composeEnvironment(t, f, b)
	source := newGitSource(t, f, b, prefix)
	enrolled, in, agentToken := enrollment(t, f, b, ws, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.BuildControls, in.Capabilities.Visual, in.Capabilities.Compose = &yes, &yes, &yes, &yes
	in.Capabilities.BuildNetwork = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	files := []composeproject.File{{Path: "compose.yaml", Content: "services:\n  web:\n    image: " + pipelineTestImage + "\n  cache:\n    image: redis:latest\n"}}
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Namespace: env.DeploymentUnit.Id, Entrypoints: []string{"compose.yaml"}, Files: files}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	web := composeproject.ResourceID(env.DeploymentUnit.Id, "service", "web").String()
	cache := composeproject.ResourceID(env.DeploymentUnit.Id, "service", "cache").String()
	cfg := pipeline.BuildConfig{ConnectionID: source.connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true, AutoDeploy: true}
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": web, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	agent.finishPipeline(agent.next(), "cancelled", pipeline.Result{})
	images := map[string]string{web: pipelineTestImage, cache: "redis" + digest('1')}
	agent.reviewAndApply(b, path, enrolled.Server.Id, draft.Id, images)

	build := func(commit string, d byte) uuid.UUID {
		t.Helper()
		source.push(commit)
		c := agent.next()
		agent.finishPipeline(c, "succeeded", pipeline.Result{Commit: commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat(string(d), 64)})
		return c.Id
	}
	run := build(strings.Repeat("b", 40), 'd')
	images[web] = "sha256:" + strings.Repeat("d", 64)
	if plan := agent.expectAutomaticDeploy(images); !strings.Contains(plan.Specification.Compose.Files[0].Content, "astilba-build-"+run.String()) {
		t.Fatal("the run's own image did not deploy")
	}

	run = build(strings.Repeat("c", 40), 'e')
	c := agent.next()
	var payload deployment.Payload
	if c.Kind != "visual" || json.Unmarshal(*c.Visual, &payload) != nil || payload.Action != "resolve" {
		t.Fatal("automatic deployment did not start with image review", c.Kind, payload.Action)
	}
	agent.observe(c, deployment.Observation{Images: map[string]string{web: "sha256:" + strings.Repeat("e", 64), cache: "redis" + digest('2')}, StateHash: strings.Repeat("d", 64)})
	note := waitBlocked(t, b, path, run)
	if !strings.Contains(note, "image tag for cache") {
		t.Fatalf("note does not name the moved service: %q", note)
	}
	if _, saved := draftSpec(t, b, path); !strings.Contains(saved.Compose.Files[0].Content, "astilba-build-"+run.String()) {
		t.Fatal("the blocked build's image was not saved to the draft")
	}
}
