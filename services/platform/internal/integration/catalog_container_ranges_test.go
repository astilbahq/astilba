package integration

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// TestStagedAllowlistLeavesOutTheServersContainers stages Kaneo, whose first account becomes its
// administrator, with a private allowlist that would also admit the server's own containers.
// Staging refuses it once the environment has a server whose agent reported its container
// ranges, and planning refuses it for such a server. A server whose agent reports no ranges
// gets a plan an owner must review, saying why, and never a silent pass.
func TestStagedAllowlistLeavesOutTheServersContainers(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Templates"}, 201, &project)
	environment := func(name string) string {
		t.Helper()
		var env api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": name}, 201, &env)
		return prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	}
	draft := func(path string) api.Revision {
		t.Helper()
		var v api.Revision
		b.call("GET", path+"/draft", nil, 200, &v)
		return v
	}
	yes := true
	ranges := []api.ContainerRange{{Prefix: "10.252.0.0/14", Source: api.ContainerRangeSourceNetworkPool}, {Prefix: "172.17.0.0/16", Source: api.ContainerRangeSourceDefaultBridge}}
	enroll := func(reports bool) (uuid.UUID, string) {
		t.Helper()
		enrolled, in, token := enrollment(t, f, b, ws, "deployment")
		in.Capabilities.Visual, in.Capabilities.ServiceOutputs = &yes, &yes
		if reports {
			in.Capabilities.ContainerRanges, in.Preflight.ContainerRanges = &yes, &ranges
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		return enrolled.Server.Id, token
	}
	reporting, reportingToken := enroll(true)
	silent, silentToken := enroll(false)
	stage := func(path, recipe, name string, allow ...string) (int, []byte) {
		t.Helper()
		in := api.CatalogStageRequest{RecipeId: recipe, BaseRevisionId: draft(path).Id, Name: name}
		if allow != nil {
			domainName := strings.ToLower(name) + ".example.com"
			in.Route = &api.CatalogStageRoute{Domain: &domainName, Tls: "managed", AllowCidrs: allow}
		}
		return b.request("POST", path+"/catalog-stagings", in)
	}
	plan := func(path string, server uuid.UUID, status int) ([]byte, deployment.Status) {
		t.Helper()
		var out deployment.Status
		raw := b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: server, BaseRevisionId: draft(path).Id}, status, &out)
		return raw, out
	}
	// resolve answers the plan's resolution as the agent would, and returns the resolved plan.
	resolve := func(path, token string, status deployment.Status) deployment.Status {
		t.Helper()
		var polled api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
		if polled.Command == nil {
			t.Fatal("no resolution command")
		}
		spec, _, err := domain.ParseSpec(draft(path).Specification)
		if err != nil {
			t.Fatal(err)
		}
		images := map[string]string{}
		for _, a := range spec.Applications {
			images[a.ID.String()] = *a.Source.Image
		}
		observation, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("a", 64), HostChangesReported: true})
		c := polled.Command
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&observation)}, 204, nil)
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		if status.Plan == nil {
			t.Fatal("plan did not resolve")
		}
		return status
	}
	refusesNaming := func(raw []byte, entry, overlaps string) {
		t.Helper()
		if !strings.Contains(string(raw), entry+" also admits this server's other containers") || !strings.Contains(string(raw), overlaps) {
			t.Fatalf("refusal does not name %s and %s: %s", entry, overlaps, raw)
		}
	}

	// Staging into an environment deployed to a reporting server refuses an overlapping entry.
	deployed := environment("Deployed")
	if code, raw := stage(deployed, "postgresql-18", "Reporting"); code != 201 {
		t.Fatal("staging a database", code, string(raw))
	}
	_, status := plan(deployed, reporting, 201)
	status = resolve(deployed, reportingToken, status)
	b.call("POST", deployed+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, nil)
	code, raw := stage(deployed, "kaneo", "Kaneo", "203.0.113.7/32", "10.0.0.0/8")
	if code != 400 {
		t.Fatal("staged an allowlist admitting the server's network pool", code)
	}
	refusesNaming(raw, "10.0.0.0/8", "the agent's network pool, 10.252.0.0/14")
	code, raw = stage(deployed, "kaneo", "Kaneo", "172.17.0.0/16")
	if code != 400 {
		t.Fatal("staged an allowlist admitting Docker's default bridge", code)
	}
	refusesNaming(raw, "172.17.0.0/16", "Docker's default bridge, 172.17.0.0/16")
	if code, raw = stage(deployed, "kaneo", "Kaneo", "203.0.113.7/32", "192.168.1.0/24"); code != 201 {
		t.Fatal("refused the owner's own networks", code, string(raw))
	}

	// With no server yet staging can't check, so planning checks the server the plan chooses.
	fresh := environment("Fresh")
	if code, raw = stage(fresh, "kaneo", "Tracker", "10.0.0.0/8"); code != 201 {
		t.Fatal("staging without a server", code, string(raw))
	}
	raw, _ = plan(fresh, reporting, 400)
	refusesNaming(raw, "10.0.0.0/8", "10.252.0.0/14")

	// A server whose agent reports no ranges gets a plan an owner reviews, which says why.
	_, status = plan(fresh, silent, 201)
	status = resolve(fresh, silentToken, status)
	if !status.HostReview || !slices.Contains(status.Plan.Effects, deployment.UnreportedContainerRangesEffect) {
		t.Fatal("an unchecked allowlist did not need review", status.HostReview, status.Plan.Effects)
	}
	b.call("POST", fresh+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New()}, 400, nil)
	// Once its agent reports them, planning refuses there too.
	heartbeat := api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Protocol: 1, Version: "test", Os: "linux", Arch: "arm64", Docker: true, Visual: &yes, ServiceOutputs: &yes, ContainerRanges: &yes}, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 1 << 30, Notes: []string{}, ContainerRanges: &ranges}}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", silentToken, heartbeat, 200, nil)
	raw, _ = plan(fresh, silent, 400)
	refusesNaming(raw, "10.0.0.0/8", "10.252.0.0/14")
	// An agent that reports the capability but could not read its ranges is reviewed as well.
	heartbeat.Preflight.ContainerRanges = nil
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", silentToken, heartbeat, 200, nil)
	_, status = plan(fresh, silent, 201)
	status = resolve(fresh, silentToken, status)
	if !status.HostReview || !slices.Contains(status.Plan.Effects, deployment.UnreadContainerRangesEffect) {
		t.Fatal("an unread report did not need review", status.HostReview, status.Plan.Effects)
	}
	// A report management can't read strictly, here a prefix with host bits set, counts as none:
	// it neither breaks planning nor passes unchecked.
	garbage := []api.ContainerRange{{Prefix: "10.252.0.1/14", Source: api.ContainerRangeSourceNetworkPool}}
	heartbeat.Preflight.ContainerRanges = &garbage
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", silentToken, heartbeat, 200, nil)
	_, status = plan(fresh, silent, 201)
	status = resolve(fresh, silentToken, status)
	if !status.HostReview || !slices.Contains(status.Plan.Effects, deployment.UnreadContainerRangesEffect) {
		t.Fatal("an unreadable report did not need review", status.HostReview, status.Plan.Effects)
	}

	// Changing the staged allowlist is an ordinary route change, which planning leaves to the
	// owner: even this server's unread ranges no longer ask for review.
	current := draft(fresh)
	spec, _, err := domain.ParseSpec(current.Specification)
	if err != nil {
		t.Fatal(err)
	}
	spec.Routes[0].AllowCIDRs = []string{"10.0.0.0/8", "203.0.113.7/32"}
	b.call("PUT", fresh+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": spec}, 200, nil)
	if _, status = plan(fresh, silent, 201); status.HostReview {
		t.Fatal("an edited allowlist still needed review")
	}
	resolve(fresh, silentToken, status)

	// From here the server reports its ranges again, so a check refuses rather than reviews.
	heartbeat.Preflight.ContainerRanges = &ranges
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", silentToken, heartbeat, 200, nil)
	// setStaged replaces the environment's record of staged routes; nil removes it, as a row
	// from before the record has none.
	setStaged := func(path string, value *string) {
		t.Helper()
		env := uuid.MustParse(path[strings.LastIndex(path, "/")+1:])
		if _, err := f.st.Pool.Exec(t.Context(), `UPDATE catalog_stagings SET staged_routes=$3::jsonb WHERE workspace_id=$1 AND environment_id=$2`, ws, env, value); err != nil {
			t.Fatal(err)
		}
	}
	reviews := func(path, effect string) {
		t.Helper()
		_, status := plan(path, silent, 201)
		status = resolve(path, silentToken, status)
		if !status.HostReview || !slices.Contains(status.Plan.Effects, effect) {
			t.Fatal("planning did not ask for review", status.HostReview, status.Plan.Effects)
		}
	}
	clone := func(path, name string) string {
		t.Helper()
		var copied api.Environment
		b.call("POST", path+"/clone", api.CloneEnvironment{Name: name}, 201, &copied)
		return prefix + "/projects/" + project.Id.String() + "/environments/" + copied.Id.String()
	}

	// A staging from before the record of its routes is worked out from the revision it saved.
	original := environment("Original")
	if code, raw = stage(original, "kaneo", "Original", "10.0.0.0/8"); code != 201 {
		t.Fatal("staging without a server", code, string(raw))
	}
	setStaged(original, nil)
	raw, _ = plan(original, silent, 400)
	refusesNaming(raw, "10.0.0.0/8", "10.252.0.0/14")

	// The owner moves the staged route to another service, keeping its allowlist, and gives the
	// staged service a route of their own. A clone starts its services empty, so its first
	// account is new: its copy of the still-staged route is checked as the original is, its
	// other route is the owner's, and it keeps the staging's backup recommendations.
	current = draft(original)
	if spec, _, err = domain.ParseSpec(current.Specification); err != nil {
		t.Fatal(err)
	}
	image := execution.TestImage
	other := domain.Application{ID: uuid.New(), Name: "Other", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{}}
	spec.Applications = append(spec.Applications, other)
	office := spec.Routes[0]
	office.ID, office.Domain, office.AllowCIDRs = uuid.New(), "office.example.com", []string{"198.51.100.0/24"}
	spec.Routes[0].TargetID = other.ID
	spec.Routes = append(spec.Routes, office)
	b.call("PUT", original+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": spec}, 200, nil)
	copied := clone(original, "Copy")
	raw, _ = plan(copied, silent, 400)
	refusesNaming(raw, "10.0.0.0/8", "10.252.0.0/14")
	var stagings []api.CatalogStaging
	b.call("GET", copied+"/catalog-stagings", nil, 200, &stagings)
	if len(stagings) != 1 || stagings[0].RecipeId != "kaneo" {
		t.Fatal("the clone did not keep its staging", stagings)
	}
	// The clone records only the still-staged route, under its own identity.
	var recorded []byte
	if err := f.st.Pool.QueryRow(t.Context(), `SELECT staged_routes FROM catalog_stagings WHERE workspace_id=$1 AND environment_id=$2`, ws, uuid.MustParse(copied[strings.LastIndex(copied, "/")+1:])).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	var kept map[string][]string
	if err := json.Unmarshal(recorded, &kept); err != nil || len(kept) != 1 {
		t.Fatal("the clone's record of staged routes", string(recorded), err)
	}
	for id, allow := range kept {
		if id == spec.Routes[0].ID.String() || !slices.Equal(allow, []string{"10.0.0.0/8"}) {
			t.Fatal("the clone recorded another route", string(recorded))
		}
	}

	// A record management can't read leaves the staged routes unknown, so planning asks for
	// review rather than passing them; a clone of it says it could not confirm them.
	unreadable := `{"not a route":["10.0.0.0/8"]}`
	setStaged(copied, &unreadable)
	reviews(copied, deployment.UnresolvedStagingEffect)
	reviews(clone(copied, "Unconfirmed copy"), deployment.UnconfirmedStagingEffect)

	// Once the owner has opened the route, as they do after creating the administrator, a clone
	// routes as the owner chose: its open route is no staged allowlist to refuse or review.
	opening := environment("Opening")
	if code, raw = stage(opening, "kaneo", "Opening", "203.0.113.7/32"); code != 201 {
		t.Fatal("staging", code, string(raw))
	}
	current = draft(opening)
	if spec, _, err = domain.ParseSpec(current.Specification); err != nil {
		t.Fatal(err)
	}
	spec.Routes[0].AllowCIDRs = []string{}
	b.call("PUT", opening+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": spec}, 200, nil)
	opened := clone(opening, "Opened copy")
	if _, status = plan(opened, silent, 201); status.HostReview {
		t.Fatal("a clone of an opened route still treated it as staged")
	}
	resolve(opened, silentToken, status)
	b.call("GET", opened+"/catalog-stagings", nil, 200, &stagings)
	if len(stagings) != 1 {
		t.Fatal("the clone lost the staging", stagings)
	}
}
