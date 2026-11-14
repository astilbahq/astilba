package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestReleasePlanHistoryAndRollbackAdmission(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Releases"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "HTTP"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app := uuid.New()
	image := "busybox:stable"
	value := "v1"
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "VALUE", Value: &value}}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "releases.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrollment, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.SafeReleases = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func() api.ExecutionCommand {
		t.Helper()
		var p api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command == nil {
			t.Fatal("missing command")
		}
		return *p.Command
	}
	report := func(c api.ExecutionCommand, obs deployment.Observation) {
		t.Helper()
		raw, _ := stablejson.Marshal(obs)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
	}
	digest := "busybox@sha256:" + strings.Repeat("a", 64)
	prepare := func(host bool, options *api.ReleaseOptions) deployment.Status {
		t.Helper()
		var status deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrollment.Server.Id, BaseRevisionId: draft.Id, ReleaseOptions: options}, 201, &status)
		report(poll(), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): digest}, StateHash: strings.Repeat("f", 64)})
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status
	}
	apply := func(status deployment.Status) {
		t.Helper()
		var op api.Operation
		key := uuid.New()
		url := path + "/deployment-plans/" + status.ID.String() + "/apply"
		b.call("POST", url, api.ApplyVisualPlan{IdempotencyKey: key, ApproveHostChanges: approved()}, 201, &op)
		var again api.Operation
		b.call("POST", url, api.ApplyVisualPlan{IdempotencyKey: key, ApproveHostChanges: approved()}, 201, &again)
		if op.Id != again.Id {
			t.Fatal("release duplicated")
		}
		c := poll()
		report(c, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: app, Kind: "service", State: "running", Image: digest}}, Applied: true, StateHash: strings.Repeat("e", 64)})
	}
	first := prepare(true, nil)
	if first.Plan == nil || first.Plan.Release != nil {
		t.Fatal("initial creation promised old traffic")
	}
	apply(first)
	// No saved-spec change: mutable image identity alone selects the protected path.
	digest = "busybox@sha256:" + strings.Repeat("b", 64)
	next := prepare(false, nil)
	if next.Plan == nil || next.Plan.Release == nil || next.Plan.Release.PreviousPlanID != first.ID || next.Plan.Instances[app.String()] == uuid.Nil {
		t.Fatal("moved tag bypassed release protection")
	}
	apply(next)
	var history []api.ReleaseHistoryEntry
	b.call("GET", path+"/releases", nil, 200, &history)
	if len(history) != 2 {
		t.Fatalf("history contains unapplied plans: %d", len(history))
	}
	wrong := prefix + "/projects/" + uuid.NewString() + "/environments/" + env.Id.String()
	b.call("GET", wrong+"/releases", nil, 404, nil)
	var rolled api.Revision
	rollbackURL := path + "/releases/" + first.ID.String() + "/rollback"
	inRollback := api.RollbackRelease{BaseRevisionId: draft.Id, ApplicationId: app}
	b.call("POST", wrong+"/releases/"+first.ID.String()+"/rollback", inRollback, 404, nil)
	b.call("POST", rollbackURL, inRollback, 201, &rolled)
	b.call("POST", rollbackURL, inRollback, 409, nil)
	var restored domain.Specification
	if json.Unmarshal(rolled.Specification, &restored) != nil || *restored.Applications[0].Source.Image != "busybox@sha256:"+strings.Repeat("a", 64) {
		t.Fatal("rollback did not freeze historical image")
	}
	draft = rolled
	digest = *restored.Applications[0].Source.Image
	rollback := prepare(false, nil)
	if rollback.Plan == nil || rollback.Plan.Release == nil || len(rollback.Plan.Release.Options.Command) != 0 {
		t.Fatal("rollback skipped protection or copied hook")
	}
	// A capability downgrade after review cannot dispatch an instance plan.
	no := false
	in.Capabilities.SafeReleases = &no
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	b.call("POST", path+"/deployment-plans/"+rollback.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 400, nil)
}

// A release names secret versions by ID. Its history says which secrets a rollback would return
// to an earlier version, without opening a value. The runtime-secret check doesn't search for a
// version only an old release names, and does once the rollback draft names it.
func TestRollbackShowsAndChecksOlderSecretVersions(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ctx := context.Background()
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Releases"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "HTTP"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var first, dropped api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "rollback-first-token"}, 201, &first)
	b.call("POST", path+"/secrets", map[string]string{"name": "DROPPED", "value": "rollback-dropped-token"}, 201, &dropped)
	app := uuid.New()
	image := "busybox:stable"
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "TOKEN", SecretVersionID: &first.Id}, {Name: "DROPPED", SecretVersionID: &dropped.Id}}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "rollback-secrets.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrollment, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.SafeReleases = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func() api.ExecutionCommand {
		t.Helper()
		var p api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command == nil {
			t.Fatal("missing command")
		}
		return *p.Command
	}
	report := func(c api.ExecutionCommand, obs deployment.Observation) {
		t.Helper()
		raw, _ := stablejson.Marshal(obs)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
	}
	deploy := func(digest string) deployment.Status {
		t.Helper()
		var status deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrollment.Server.Id, BaseRevisionId: draft.Id}, 201, &status)
		report(poll(), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): digest}, StateHash: strings.Repeat("f", 64)})
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, nil)
		report(poll(), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: app, Kind: "service", State: "running", Image: digest}}, Applied: true, StateHash: strings.Repeat("e", 64)})
		return status
	}
	released := deploy("busybox@sha256:" + strings.Repeat("a", 64))
	// Three rotations take the first value out of the versions searched for whatever names them.
	var current api.SecretVersion
	for _, value := range []string{"rollback-second-token", "rollback-third-token", "rollback-fourth-token"} {
		b.call("POST", path+"/secrets/"+first.SecretId.String()+"/versions", map[string]string{"value": value}, 201, &current)
	}
	spec.Applications[0].Variables = []domain.Variable{{Name: "TOKEN", SecretVersionID: &current.Id}}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	deploy("busybox@sha256:" + strings.Repeat("b", 64))
	// The second release no longer uses DROPPED, and it is retired as an owner can once the first
	// release's plan has left its review window: the secret then has no current version.
	if _, err := f.st.Pool.Exec(ctx, "UPDATE secrets SET retired_at=now() WHERE workspace_id=$1 AND id=$2", ws, dropped.SecretId); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE secret_versions SET encrypted_value=NULL,plaintext_bytes=NULL,json_bytes=NULL,erased_at=now() WHERE workspace_id=$1 AND secret_id=$2", ws, dropped.SecretId); err != nil {
		t.Fatal(err)
	}

	var history []api.ReleaseHistoryEntry
	b.call("GET", path+"/releases", nil, 200, &history)
	if len(history) != 2 || len(history[0].Applications[0].OlderSecretVersions) != 0 {
		t.Fatalf("the running release lists older secret versions: %+v", history)
	}
	older := history[1].Applications[0].OlderSecretVersions
	if history[1].PlanId != released.ID || len(older) != 2 || older[0].VersionId != dropped.Id || older[0].Name != "DROPPED" || !older[0].Retired || older[1].VersionId != first.Id || older[1].SecretId != first.SecretId || older[1].Name != "TOKEN" || older[1].Retired {
		t.Fatalf("the earlier release's older secret versions: %+v", older)
	}
	searched := func() map[uuid.UUID]bool {
		t.Helper()
		q := db.New(f.st.Pool)
		used, err := q.EnvironmentSecretVersionsInUse(ctx, db.EnvironmentSecretVersionsInUseParams{WorkspaceID: ws, EnvironmentID: env.Id})
		if err != nil {
			t.Fatal(err)
		}
		named := []uuid.UUID{}
		for _, v := range used {
			named = append(named, v.ID)
		}
		rows, err := q.RuntimeSecretCheckCandidates(ctx, db.RuntimeSecretCheckCandidatesParams{WorkspaceID: ws, EnvironmentID: env.Id, Named: named, Recent: 3})
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]bool{}
		for _, row := range rows {
			out[row.ID] = row.Current || row.Named
		}
		return out
	}
	if _, ok := searched()[first.Id]; ok {
		t.Fatal("a version only an earlier release names is searched for")
	}
	var rolled api.Revision
	b.call("POST", path+"/releases/"+released.ID.String()+"/rollback", api.RollbackRelease{BaseRevisionId: draft.Id, ApplicationId: app}, 201, &rolled)
	if required, ok := searched()[first.Id]; !ok || !required {
		t.Fatal("the version the rollback draft names is not searched for as required")
	}
}
