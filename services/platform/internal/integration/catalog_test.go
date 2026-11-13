package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// TestAppTemplateStagesWithGeneratedSecretsAndDeploys stages Kaneo through the API: the draft
// gains the connected services and an allowlisted route, the secret store gains generated values
// that no response carries, and the assigned agent alone receives them for deployment.
func TestAppTemplateStagesWithGeneratedSecretsAndDeploys(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var address api.ClientAddress
	b.call("GET", prefix+"/client-address", nil, 200, &address)
	if address.Prefix == nil || *address.Prefix != "127.0.0.1/32" {
		t.Fatal("client address", address.Prefix)
	}
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Templates"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Tracker"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	stagings := path + "/catalog-stagings"
	domainName := "tracker.example.com"
	request := func(base uuid.UUID, name string) api.CatalogStageRequest {
		return api.CatalogStageRequest{RecipeId: "kaneo", BaseRevisionId: base, Name: name, Route: &api.CatalogStageRoute{Domain: &domainName, Tls: "managed", AllowCidrs: []string{"203.0.113.7/32"}}}
	}

	// The route is required, and so is its allowlist.
	open := request(env.DraftRevisionId, "Kaneo")
	open.Route.AllowCidrs = []string{}
	b.call("POST", stagings, open, 400, nil)
	everyone := request(env.DraftRevisionId, "Kaneo")
	everyone.Route.AllowCidrs = []string{"0.0.0.0/0"}
	if refusal := b.call("POST", stagings, everyone, 400, nil); !strings.Contains(string(refusal), "Widen the route after that") {
		t.Fatal("open allowlist refused without saying how to open the route later", string(refusal))
	}
	unrouted := request(env.DraftRevisionId, "Kaneo")
	unrouted.Route = nil
	b.call("POST", stagings, unrouted, 400, nil)
	b.call("POST", stagings, request(uuid.New(), "Kaneo"), 409, nil)

	var saved api.SavedDraft
	raw := b.call("POST", stagings, request(env.DraftRevisionId, "Kaneo"), 201, &saved)
	spec, _, err := domain.ParseSpec(*saved.Specification)
	if err != nil {
		t.Fatal(err)
	}
	var draft api.Revision
	b.call("GET", path+"/draft", nil, 200, &draft)
	if draft.Id != saved.Id || len(spec.Applications) != 2 || len(spec.Bindings) != 1 || len(spec.Routes) != 1 {
		t.Fatal("staging did not save the template as the draft")
	}
	kaneo, database := spec.Applications[0], spec.Applications[1]
	route := spec.Routes[0]
	if route.TargetID != kaneo.ID || route.Domain != domainName || route.AllowCIDRs[0] != "203.0.113.7/32" || database.Postgres == nil {
		t.Fatal("route or database", route)
	}
	var secrets []api.SecretVersion
	b.call("GET", path+"/secrets", nil, 200, &secrets)
	names := map[string]uuid.UUID{}
	for _, s := range secrets {
		names[s.Name] = s.Id
	}
	if names["Kaneo · AUTH_SECRET"] == uuid.Nil || names["Kaneo database · password"] != database.Postgres.PasswordSecretVersionID || len(names) != 2 {
		t.Fatal("generated secrets", names)
	}

	// Staging again needs other names, and can use an existing secret instead of a new one.
	b.call("POST", stagings, request(draft.Id, "Kaneo"), 400, nil)
	second := request(draft.Id, "Second")
	other := "second.example.com"
	second.Route.Domain = &other
	password := catalogPassword(t)
	second.ExistingSecrets = &[]struct {
		SecretId        uuid.UUID `json:"secretId"`
		SecretVersionId uuid.UUID `json:"secretVersionId"`
	}{{SecretId: password, SecretVersionId: names["Kaneo database · password"]}}
	b.call("POST", stagings, second, 201, &saved)
	b.call("GET", path+"/secrets", nil, 200, &secrets)
	if len(secrets) != 3 {
		t.Fatal("staging generated a secret it was given", len(secrets))
	}
	var listed []api.CatalogStaging
	b.call("GET", stagings, nil, 200, &listed)
	// Nothing is deployed yet, so nothing is recommended: a policy needs the applied deployment.
	if len(listed) != 2 || len(listed[1].Backups) != 0 || listed[0].RecipeName != "Kaneo" || listed[0].RecipeVersion != "2.49.0" || listed[0].RevisionId != saved.Id {
		t.Fatal("staging records", listed)
	}
	// The second staging is undone before deployment; the first stays.
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": saved.Id, "specification": draft.Specification}, 200, &saved)

	// A staging that generates nothing opens no keys and stores no secret.
	seals := func() (n int) {
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM secret_access WHERE workspace_id=$1 AND purpose='seal'", ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := seals()
	pg, _ := catalog.Find("postgresql-18")
	given := []struct {
		SecretId        uuid.UUID `json:"secretId"`
		SecretVersionId uuid.UUID `json:"secretVersionId"`
	}{{SecretId: pg.Secrets[0].ID, SecretVersionId: names["Kaneo database · password"]}}
	b.call("POST", stagings, api.CatalogStageRequest{RecipeId: pg.ID, BaseRevisionId: saved.Id, Name: "Reporting", ExistingSecrets: &given}, 201, &saved)
	b.call("GET", path+"/secrets", nil, 200, &secrets)
	if seals() != before || len(secrets) != 3 {
		t.Fatal("staging with given secrets opened keys or stored a secret")
	}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": saved.Id, "specification": draft.Specification}, 200, &saved)

	// Only a writer stages, and only into a dashboard-owned environment.
	b.call("POST", prefix+"/invitations", map[string]string{"email": "catalog-viewer@example.test", "role": "viewer"}, 201, nil)
	viewer := f.browser(t)
	var session api.Session
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, f, "/invitation"), "name": "Viewer", "password": f.password}, 200, &session)
	viewer.csrf = session.CsrfToken
	viewer.call("POST", stagings, request(saved.Id, "Viewer"), 403, nil)
	var compose api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &compose)
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments/"+compose.Id.String()+"/catalog-stagings", request(compose.DraftRevisionId, "Kaneo"), 400, nil)

	// The assigned agent receives the generated values; nothing the browser read holds them.
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.ServiceOutputs = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	var status deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: saved.Id}, 201, &status)
	var polled api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	if polled.Command == nil {
		t.Fatal("no resolution command")
	}
	images := map[string]string{kaneo.ID.String(): *kaneo.Source.Image, database.ID.String(): *database.Source.Image}
	observation, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("a", 64)})
	c := polled.Command
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&observation)}, 204, nil)
	b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
	var operation api.Operation
	b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &operation)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	var payload deployment.Payload
	if polled.Command == nil || json.Unmarshal(*polled.Command.Visual, &payload) != nil {
		t.Fatal("no deployment command")
	}
	auth := payload.Secrets[names["Kaneo · AUTH_SECRET"].String()]
	dbPassword := payload.Secrets[database.Postgres.PasswordSecretVersionID.String()]
	if len(auth) != 64 || strings.Trim(auth, "0123456789abcdef") != "" || len(dbPassword) != 43 {
		t.Fatal("agent did not receive the generated values")
	}
	resources := []deployment.Resource{}
	for _, a := range []domain.Application{kaneo, database} {
		resources = append(resources, deployment.Resource{ID: a.ID, Kind: a.ServiceKind(), State: "running", Image: images[a.ID.String()]})
	}
	deployed, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: resources, Images: images, Applied: true, StateHash: strings.Repeat("b", 64)})
	c = polled.Command
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&deployed)}, 204, nil)
	// The deployed database now has its recommended backup; the undone stagings have none.
	b.call("GET", stagings, nil, 200, &listed)
	first := listed[len(listed)-1]
	if len(listed) != 3 || len(first.Backups) != 1 || first.Backups[0].ServiceId != database.ID || first.Backups[0].Method != "postgres-logical" || len(listed[0].Backups)+len(listed[1].Backups) != 0 {
		t.Fatal("backup presets after deployment", listed)
	}
	for _, seen := range [][]byte{raw, b.call("GET", path+"/draft", nil, 200, nil), b.call("GET", path+"/secrets", nil, 200, nil), b.call("GET", stagings, nil, 200, nil), b.call("GET", path+"/revisions", nil, 200, nil)} {
		if strings.Contains(string(seen), auth) || strings.Contains(string(seen), dbPassword) {
			t.Fatal("a browser response holds a generated secret")
		}
	}
}

// catalogPassword is the Kaneo template's placeholder for its database password.
func catalogPassword(t *testing.T) uuid.UUID {
	t.Helper()
	kaneo, ok := catalog.Find("kaneo")
	if !ok {
		t.Fatal("no Kaneo template")
	}
	for _, s := range kaneo.Secrets {
		if s.Name == "password" {
			return s.ID
		}
	}
	t.Fatal("no database password")
	return uuid.Nil
}
