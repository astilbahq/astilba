package integration

import (
	"context"
	"encoding/json/jsontext"
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

// Planning checks a plan's secrets fit from their stored lengths and opens none of them. A
// version without lengths, written before they were stored, is opened to measure it until the
// measuring job stores them.
func TestPlanningMeasuresSecretsWithoutOpeningThem(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Lengths"}, 201, &project)

	// environment saves a draft whose Postgres password is a secret, and returns how to plan it.
	environment := func(name, value string) (uuid.UUID, func(status int)) {
		t.Helper()
		var env api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": name}, 201, &env)
		path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
		var secret api.SecretVersion
		b.call("POST", path+"/secrets", map[string]string{"name": "DB_PASSWORD", "value": value}, 201, &secret)
		app, pg, volume := uuid.New(), uuid.New(), uuid.New()
		image, pgimage := execution.TestImage, domain.PostgresImage
		spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}, {ID: pg, Name: "Postgres", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgimage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Bindings: []domain.Binding{}, Routes: []domain.Route{}}
		var draft api.Revision
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
		return secret.Id, func(status int) {
			t.Helper()
			var plan deployment.Status
			b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, status, &plan)
			if status != 201 {
				return
			}
			// The server resolves one plan at a time: this one finishes before the next.
			var poll api.AgentPoll
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &poll)
			if poll.Command == nil || poll.Command.Id != plan.OperationID {
				t.Fatal("plan resolution not delivered")
			}
			c := poll.Command
			obs := deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): image, pg.String(): pgimage}, StateHash: strings.Repeat("a", 64)}
			raw, _ := stablejson.Marshal(obs)
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
		}
	}
	// Planning opens values for the account that plans; the measuring job, which may also run at
	// any time, records itself.
	inspected := func() int {
		t.Helper()
		var n int
		if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM secret_access WHERE workspace_id=$1 AND purpose='inspect' AND principal_kind='account'`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Saving a value stores its lengths, so planning opens nothing.
	value := `pass "word"`
	stored, plan := environment("Stored", value)
	var plain, encoded int
	if err := f.st.Pool.QueryRow(ctx, `SELECT plaintext_bytes,json_bytes FROM secret_versions WHERE id=$1`, stored).Scan(&plain, &encoded); err != nil {
		t.Fatal(err)
	}
	if wantPlain, wantJSON := domain.SecretLengths([]byte(value)); plain != wantPlain || encoded != wantJSON {
		t.Fatalf("stored lengths %d/%d, want %d/%d", plain, encoded, wantPlain, wantJSON)
	}
	before := inspected()
	plan(201)
	if inspected() != before {
		t.Fatal("planning opened a secret whose lengths are stored")
	}

	// A version without lengths is opened to measure it, until the job stores them.
	unmeasured, planUnmeasured := environment("Unmeasured", "legacy-value")
	if _, err := f.st.Pool.Exec(ctx, `UPDATE secret_versions SET plaintext_bytes=NULL,json_bytes=NULL WHERE id=$1`, unmeasured); err != nil {
		t.Fatal(err)
	}
	before = inspected()
	planUnmeasured(201)
	if inspected() != before+1 {
		t.Fatalf("planning an unmeasured version made %d inspect records", inspected()-before)
	}
	if more, err := f.execution.MeasureSecretLengths(ctx); err != nil || more {
		t.Fatalf("measuring: more %v, %v", more, err)
	}
	if err := f.st.Pool.QueryRow(ctx, `SELECT plaintext_bytes,json_bytes FROM secret_versions WHERE id=$1`, unmeasured).Scan(&plain, &encoded); err != nil || plain != len("legacy-value") {
		t.Fatalf("measured %d: %v", plain, err)
	}
	var job string
	if err := f.st.Pool.QueryRow(ctx, `SELECT job FROM secret_access WHERE workspace_id=$1 AND purpose='inspect' ORDER BY at DESC,id DESC LIMIT 1`, ws).Scan(&job); err != nil || job != "secret-lengths" {
		t.Fatalf("measuring was not recorded as its job: %q %v", job, err)
	}

	// A version that can't be opened stays unmeasured and holds up no other.
	broken := uuid.UUID{15: 1}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secret_versions(workspace_id,secret_id,id,encrypted_value,author_id) SELECT workspace_id,secret_id,$2,'\x00',author_id FROM secret_versions WHERE id=$1`, stored, broken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE secret_versions SET plaintext_bytes=NULL,json_bytes=NULL WHERE id=$1`, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.MeasureSecretLengths(ctx); err != nil {
		t.Fatal(err)
	}
	var brokenMeasured, storedMeasured bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT (SELECT plaintext_bytes IS NOT NULL FROM secret_versions WHERE id=$1),(SELECT plaintext_bytes IS NOT NULL FROM secret_versions WHERE id=$2)`, broken, stored).Scan(&brokenMeasured, &storedMeasured); err != nil || brokenMeasured || !storedMeasured {
		t.Fatalf("broken measured %v, its neighbour measured %v: %v", brokenMeasured, storedMeasured, err)
	}

	// The limit still holds, from the stored lengths.
	tooLarge, planTooLarge := environment("Too large", "small")
	if _, err := f.st.Pool.Exec(ctx, `UPDATE secret_versions SET plaintext_bytes=70000,json_bytes=70002 WHERE id=$1`, tooLarge); err != nil {
		t.Fatal(err)
	}
	before = inspected()
	planTooLarge(400)
	if inspected() != before {
		t.Fatal("refusing an oversized plan opened a secret")
	}
}
