package integration

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// The dashboard reads the PostgreSQL releases from the server. A retired release stays valid for a
// database that runs it, but a draft cannot start a new database on one.
func TestPostgresReleasesAndRetiredReleaseDrafts(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var releases []api.PostgresRelease
	b.call("GET", prefix+"/postgres-releases", nil, 200, &releases)
	b.call("GET", "/api/v1/workspaces/"+uuid.NewString()+"/postgres-releases", nil, 404, nil)
	if len(releases) != len(domain.PostgresReleases()) {
		t.Fatalf("listed %d releases", len(releases))
	}
	var retired api.PostgresRelease
	current := map[int]string{}
	for _, r := range releases {
		known, ok := domain.PostgresReleaseOf(r.Image)
		if !ok || r.Name != known.Name() || r.Major != known.Major() || r.Mount != known.Mount || r.Current == known.Retired() {
			t.Fatalf("release %+v differs from the server's", r)
		}
		if r.Current {
			current[r.Major] = r.Image
		} else if r.Major == 18 {
			retired = r
		}
	}
	if current[18] != domain.PostgresImage || current[17] != domain.Postgres17Image || retired.Image == "" {
		t.Fatalf("current releases %v, retired %+v", current, retired)
	}
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Releases"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Database"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "Database password", "value": "releases-password-canary"}, 201, &secret)
	spec := func(image, mount string) domain.Specification {
		pg, vol := uuid.New(), uuid.New()
		return domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Username: "app", Database: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: mount}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
	}
	refused := string(b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec(retired.Image, retired.Mount)}, 400, nil))
	if !strings.Contains(refused, "applications.postgres.retired") || !strings.Contains(refused, "is retired, so Database cannot start on it") {
		t.Fatalf("a new database started on a retired release: %s", refused)
	}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec(domain.PostgresImage, domain.PostgresMount(domain.PostgresImage))}, 200, &draft)
	// An agent from before the release list would refuse the plan as invalid; management says why.
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.PostgresReleases = &yes, nil
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	request := api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}
	refused = string(b.call("POST", path+"/deployment-plans", request, 400, nil))
	if !strings.Contains(refused, "before running PostgreSQL "+retiredSuccessor(releases)+" there: its agent does not know that release") {
		t.Fatalf("an agent that does not know the release was sent it: %s", refused)
	}
	in.Capabilities.PostgresReleases = agentPostgresReleases()
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	b.call("POST", path+"/deployment-plans", request, 201, nil)
}

func retiredSuccessor(releases []api.PostgresRelease) string {
	for _, r := range releases {
		if r.Current && r.Major == 18 {
			return r.Name
		}
	}
	return ""
}

// agentPostgresReleases is what a current agent advertises: every release it knows.
func agentPostgresReleases() *[]string {
	out := []string{}
	for _, r := range domain.PostgresReleases() {
		out = append(out, r.Image)
	}
	return &out
}

// refuseOlderMember shows a cross-host plan refused, naming the member, when a member's agent does
// not know the database's release: every member validates the whole specification. The member's
// background heartbeat drops the list while flag holds its number.
func refuseOlderMember(t *testing.T, f *fixture, b *browser, flag *atomic.Int32, member execution.Identity, in api.AgentEnrollment, name, path string, draft, primary uuid.UUID, ready func()) {
	t.Helper()
	flag.Store(2)
	older := in.Capabilities
	older.PostgresReleases = nil
	if _, err := f.execution.Heartbeat(context.Background(), member, api.AgentHeartbeat{Capabilities: older, Preflight: in.Preflight}); err != nil {
		t.Fatal(err)
	}
	ready()
	if refused := string(b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft, ServerId: primary}, 400, nil)); !strings.Contains(refused, "Update the agent on "+name+" before running PostgreSQL") {
		t.Fatalf("a member that does not know the release was planned onto: %s", refused)
	}
	flag.Store(0)
	if _, err := f.execution.Heartbeat(context.Background(), member, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}); err != nil {
		t.Fatal(err)
	}
}
