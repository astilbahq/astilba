package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// This fixture crosses the real HTTP, tenant, PostgreSQL and queue boundaries.
// Host effects are covered separately by TestNativeVisualDeployment.
func TestVisualPlanAdmissionAndPortability(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Visual"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "DB_PASSWORD", "value": "visual-secret-canary"}, 201, &secret)
	app, pg, volume := uuid.New(), uuid.New(), uuid.New()
	image := execution.TestImage
	pgimage := domain.PostgresImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}, {ID: pg, Name: "Postgres", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgimage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Bindings: []domain.Binding{{ID: uuid.New(), SourceID: pg, TargetID: app, Variable: "DATABASE_URL"}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			var p api.AgentPoll
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
			if p.Command != nil && p.Command.Id == id {
				return *p.Command
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("operation not dispatched")
		return api.ExecutionCommand{}
	}
	report := func(c api.ExecutionCommand, obs deployment.Observation, state api.ExecutionState) {
		t.Helper()
		raw, _ := json.Marshal(obs)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: state, Steps: []api.ExecutionStep{}, Observation: (*json.RawMessage)(&raw)}, 204, nil)
	}
	create := func() deployment.Status {
		t.Helper()
		var s deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id, AllowHostChanges: true}, 201, &s)
		c := poll(s.OperationID)
		if strings.Contains(string(*c.Visual), "visual-secret-canary") {
			t.Fatal("resolution received secret values")
		}
		images := map[string]string{}
		for _, service := range spec.Applications {
			images[service.ID.String()] = *service.Source.Image
		}
		report(c, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("a", 64)}, "succeeded")
		b.call("GET", path+"/deployment-plans/"+s.ID.String(), nil, 200, &s)
		if s.Plan == nil {
			t.Fatal("resolved plan missing")
		}
		return s
	}
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 400, nil)
	stale := create()
	old := draft.Id
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": old, "specification": spec}, 200, &draft)
	b.call("POST", path+"/deployment-plans/"+stale.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 409, nil)
	ready := create()
	applyPath := path + "/deployment-plans/" + ready.ID.String() + "/apply"
	key := uuid.New()
	var op, again api.Operation
	b.call("POST", applyPath, api.ApplyPlan{IdempotencyKey: key}, 201, &op)
	b.call("POST", applyPath, api.ApplyPlan{IdempotencyKey: key}, 201, &again)
	if op.Id != again.Id {
		t.Fatal("apply duplicated operation")
	}
	command := poll(op.Id)
	var payload deployment.Payload
	if json.Unmarshal(*command.Visual, &payload) != nil || payload.Secrets[secret.Id.String()] != "visual-secret-canary" {
		t.Fatal("assigned executor did not receive frozen secret")
	}
	var stored []byte
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT command FROM execution_operations WHERE id=$1", op.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "visual-secret-canary") {
		t.Fatal("command stored plaintext secret")
	}
	// A controller-first upgrade must still cancel an existing agent's exact
	// hydrated command. New agents explicitly opt in to credential-free cancels.
	b.call("POST", prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
	legacyCancel := poll(op.Id)
	if !legacyCancel.CancelRequested || string(*legacyCancel.Visual) != string(*command.Visual) {
		t.Fatal("legacy visual cancellation changed the accepted payload")
	}
	in.Capabilities.CredentialFreeVisualCancel = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	currentCancel := poll(op.Id)
	if !currentCancel.CancelRequested || strings.Contains(string(*currentCancel.Visual), "visual-secret-canary") {
		t.Fatal("capable agent did not receive credential-free cancellation")
	}
	// Admission reserves PostgreSQL initialization even if an initial deployment is cancelled.
	report(command, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}, "cancelled")
	var inspection api.Operation
	b.call("POST", path+"/runtime/refresh", nil, 201, &inspection)
	inspectionCommand := poll(inspection.Id)
	var inspectionPayload deployment.Payload
	if json.Unmarshal(*inspectionCommand.Visual, &inspectionPayload) != nil || inspectionPayload.Action != "observe" || inspectionPayload.Plan.ID != ready.ID {
		t.Fatal("runtime inspection lost the unsuccessful first deployment plan")
	}
	report(inspectionCommand, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}, "succeeded")
	spec.Applications[1].Postgres.Username = "different"
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id, AllowHostChanges: true}, 400, nil)
	spec.Applications[1].Postgres.Username = "app"
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	ready = create()
	b.call("POST", path+"/deployment-plans/"+ready.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	command = poll(op.Id)
	report(command, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: app, Kind: "service", State: "running", Image: image}, {ID: pg, Kind: "service", State: "healthy", Image: pgimage}}, Applied: true, StateHash: strings.Repeat("b", 64)}, "succeeded")
	var exported struct{ Compose, Routes, Readme string }
	raw := b.call("GET", path+"/export", nil, 200, &exported)
	if strings.Contains(string(raw), "visual-secret-canary") || !strings.Contains(exported.Compose, "ASTILBA_SECRET_") {
		t.Fatal("export exposed values or omitted placeholders")
	}
	var cloned api.Environment
	b.call("POST", path+"/clone", api.CloneEnvironment{Name: "Preview", CopySecrets: false}, 201, &cloned)
	clonePath := prefix + "/projects/" + project.Id.String() + "/environments/" + cloned.Id.String()
	var cloneRevision api.Revision
	b.call("GET", clonePath+"/draft", nil, 200, &cloneRevision)
	var cloneSpec domain.Specification
	if json.Unmarshal(cloneRevision.Specification, &cloneSpec) != nil {
		t.Fatal("clone spec invalid")
	}
	if cloned.DeploymentUnit.Id == env.DeploymentUnit.Id || cloned.DeploymentUnit.ServerId != nil || cloneSpec.Volumes[0].ID == volume || cloneSpec.Bindings[0].SourceID == pg || cloneSpec.Applications[1].Postgres.PasswordSecretVersionID == secret.Id {
		t.Fatal("clone retained production identity")
	}
	var volumes []api.ManagedVolume
	b.call("GET", path+"/volumes", nil, 200, &volumes)
	if len(volumes) != 1 || volumes[0].Id != volume || !volumes[0].InUse || volumes[0].Deleted {
		t.Fatal("managed volume inventory lost attachment or ownership")
	}
	// Another environment on this server cannot take an existing route claim.
	cloneSpec.Routes[0].Domain = spec.Routes[0].Domain
	var collisionRevision api.Revision
	b.call("PUT", clonePath+"/draft", map[string]any{"baseRevisionId": cloneRevision.Id, "specification": cloneSpec}, 200, &collisionRevision)
	var collision deployment.Status
	b.call("POST", clonePath+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: collisionRevision.Id, AllowHostChanges: true}, 201, &collision)
	collisionCommand := poll(collision.OperationID)
	images := map[string]string{}
	for _, app := range cloneSpec.Applications {
		images[app.ID.String()] = *app.Source.Image
	}
	report(collisionCommand, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("a", 64)}, "succeeded")
	b.call("POST", clonePath+"/deployment-plans/"+collision.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 409, nil)
	cloneRevision = collisionRevision
	b.call("DELETE", path+"/volumes/"+volume.String(), nil, 409, nil)
	// A different environment cannot bind a secret version from the source.
	cloneSpec.Applications[1].Postgres.PasswordSecretVersionID = secret.Id
	b.call("PUT", clonePath+"/draft", map[string]any{"baseRevisionId": cloneRevision.Id, "specification": cloneSpec}, 400, nil)
	// A viewer cannot apply or destroy data, even with a valid plan identifier.
	b.call("POST", prefix+"/invitations", map[string]string{"email": "viewer@example.test", "role": "viewer"}, 201, nil)
	invite := tokenFromLinkMail(t, f, "/invitation")
	viewer := f.browser(t)
	var session api.Session
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": invite, "name": "Viewer", "password": f.password}, 200, &session)
	viewer.csrf = session.CsrfToken
	viewer.call("POST", applyPath, api.ApplyPlan{IdempotencyKey: uuid.New()}, 403, nil)
	viewer.call("DELETE", path+"/volumes/"+volume.String(), nil, 403, nil)
	// Failed deletion does not mark storage as deleted; success is recorded only from its own operation.
	retainedPG := spec.Applications[1]
	spec.Applications = spec.Applications[:1]
	spec.Bindings = nil
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	removed := create()
	b.call("POST", path+"/deployment-plans/"+removed.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	report(poll(op.Id), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: app, Kind: "service", State: "running", Image: image}}, Applied: true, StateHash: strings.Repeat("b", 64)}, "succeeded")
	// Removing the service does not erase the retained volume's initialization contract.
	retainedPG.Postgres.Username = "different"
	spec.Applications = append(spec.Applications, retainedPG)
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id, AllowHostChanges: true}, 400, nil)
	spec.Applications = spec.Applications[:1]
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	var removal api.Operation
	b.call("DELETE", path+"/volumes/"+volume.String(), nil, 201, &removal)
	removeCommand := poll(removal.Id)
	report(removeCommand, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("b", 64)}, "failed")
	b.call("GET", path+"/volumes", nil, 200, &volumes)
	if volumes[0].Deleted {
		t.Fatal("failed deletion discarded storage ownership")
	}
	b.call("DELETE", path+"/volumes/"+volume.String(), nil, 201, &removal)
	removeCommand = poll(removal.Id)
	report(removeCommand, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("b", 64)}, "succeeded")
	b.call("GET", path+"/volumes", nil, 200, &volumes)
	if !volumes[0].Deleted {
		t.Fatal("successful deletion was not recorded")
	}

}

func TestEnvironmentEventsAreScopedInvalidationHints(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	var project api.Project
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Events"}, 201, &project)
	base := "/api/v1/workspaces/" + ws.String() + "/projects/" + project.Id.String() + "/environments"
	var env api.Environment
	b.call("POST", base, map[string]string{"name": "Test"}, 201, &env)
	path := base + "/" + env.Id.String()
	b.call("GET", base+"/"+uuid.NewString()+"/events", nil, 404, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	req, err := newRequest(b, "GET", path+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(ctx)
	response, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("event stream unavailable")
	}
	stream := bufio.NewReader(response.Body)
	hint := func() {
		t.Helper()
		for _, want := range []string{"event: changed\n", "data: {}\n", "\n"} {
			line, err := stream.ReadString('\n')
			if err != nil || line != want {
				t.Fatalf("invalid hint: %q %v", line, err)
			}
		}
	}
	hint()
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}}, 200, nil)
	hint()
}
