package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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

type releaseFixture struct {
	f        *fixture
	b        *browser
	prefix   string
	identity execution.Identity
	op       uuid.UUID
	ws       uuid.UUID
	secret   uuid.UUID
}

// newReleaseFixture applies a plan whose deployment carries one secret, and returns before the
// deployment command is polled.
func newReleaseFixture(t *testing.T, acknowledges bool) *releaseFixture {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Releases"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "DB_PASSWORD", "value": "release-canary"}, 201, &secret)
	app, pg, volume := uuid.New(), uuid.New(), uuid.New()
	image, pgimage := execution.TestImage, domain.PostgresImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}, {ID: pg, Name: "Postgres", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgimage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Bindings: []domain.Binding{}, Routes: []domain.Route{}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, _ := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.AcknowledgesReleases = &acknowledges
	if _, err := f.execution.Enroll(ctx, in, "test"); err != nil {
		t.Fatal(err)
	}
	identity, err := f.execution.Authenticate(ctx, in.ServerId, *in.Credential)
	if err != nil {
		t.Fatal(err)
	}
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &plan)
	resolve := mustPoll(t, f, identity, plan.OperationID, acknowledges)
	if acknowledges {
		obs := deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): image, pg.String(): pgimage}, StateHash: strings.Repeat("a", 64)}
		raw, _ := stablejson.Marshal(obs)
		if err = f.execution.Report(ctx, identity, api.AgentReport{Id: resolve.Id, Generation: resolve.Generation, PlanHash: resolve.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}); err != nil {
			t.Fatal(err)
		}
		_ = acknowledgeRelease(ctx, f.execution, identity, resolve)
	}
	r := &releaseFixture{f: f, b: b, prefix: prefix, identity: identity, ws: ws, secret: secret.Id}
	if acknowledges {
		var op api.Operation
		b.call("GET", path+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
		b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
		r.op = op.Id
	} else {
		r.op = plan.OperationID
	}
	return r
}

// mustPoll polls until the operation is delivered, without acknowledging it.
func mustPoll(t *testing.T, f *fixture, a execution.Identity, op uuid.UUID, want bool) *api.ExecutionCommand {
	t.Helper()
	c := pollOnce(t, f, a, 3*time.Second)
	if want && (c == nil || c.Id != op) {
		t.Fatalf("operation %s not delivered: %+v", op, c)
	}
	return c
}

// pollOnce polls with a short wait, for a poll that may deliver nothing.
func pollOnce(t *testing.T, f *fixture, a execution.Identity, wait time.Duration) *api.ExecutionCommand {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c, err := f.execution.Poll(ctx, a)
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	return c
}

func (r *releaseFixture) records(t *testing.T) []string {
	t.Helper()
	rows, err := r.f.st.Pool.Query(context.Background(), `SELECT reason FROM secret_access WHERE workspace_id=$1 AND purpose='deliver' AND operation_id=$2 ORDER BY at,id`, r.ws, r.op)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var reason string
		if err = rows.Scan(&reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, reason)
	}
	return out
}

func secretOf(t *testing.T, c *api.ExecutionCommand, id uuid.UUID) string {
	t.Helper()
	var payload deployment.Payload
	if c.Visual == nil || json.Unmarshal(*c.Visual, &payload) != nil {
		t.Fatal("no visual payload")
	}
	return payload.Secrets[id.String()]
}

// A command's values go to an agent instance once, recorded once. Polls while that release may
// still be on its way open nothing; once acknowledged, the command comes without values and
// opens nothing; a lost journal asks for them again, recorded as a re-delivery.
func TestSecretsAreReleasedOncePerInstance(t *testing.T) {
	r := newReleaseFixture(t, true)
	ctx := context.Background()
	first := mustPoll(t, r.f, r.identity, r.op, true)
	if first.SecretRelease == nil || !first.SecretRelease.Values || secretOf(t, first, r.secret) != "release-canary" {
		t.Fatalf("first release: %+v", first.SecretRelease)
	}
	if got := r.records(t); len(got) != 1 || got[0] != "first-release" {
		t.Fatalf("records after the first release: %v", got)
	}
	// Within the lease another poll may still be delivering it: nothing is opened.
	if c := pollOnce(t, r.f, r.identity, 1500*time.Millisecond); c != nil && c.Id == r.op {
		t.Fatalf("released again within the lease: %+v", c.SecretRelease)
	}
	if got := r.records(t); len(got) != 1 {
		t.Fatalf("a poll within the lease opened values: %v", got)
	}
	if err := acknowledgeRelease(ctx, r.f.execution, r.identity, first); err != nil {
		t.Fatal(err)
	}
	held := mustPoll(t, r.f, r.identity, r.op, true)
	if held.SecretRelease == nil || held.SecretRelease.Values || held.SecretRelease.Digest != first.SecretRelease.Digest || secretOf(t, held, r.secret) != "" {
		t.Fatalf("acknowledged release came with values: %+v", held.SecretRelease)
	}
	if got := r.records(t); len(got) != 1 {
		t.Fatalf("an acknowledged release opened values: %v", got)
	}
	// A journal that lost the values asks for them again.
	if err := r.f.execution.NoticeSecretReleases(ctx, r.identity, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: r.op, Epoch: first.SecretRelease.Epoch, Reason: api.JournalMissing}}}); err != nil {
		t.Fatal(err)
	}
	again := mustPoll(t, r.f, r.identity, r.op, true)
	if again.SecretRelease == nil || !again.SecretRelease.Values || secretOf(t, again, r.secret) != "release-canary" {
		t.Fatalf("re-delivery: %+v", again.SecretRelease)
	}
	if got := r.records(t); len(got) != 2 || got[1] != "redelivery" {
		t.Fatalf("records after re-delivery: %v", got)
	}
}

// An unacknowledged release is made again after the lease, up to a limit. Then the command
// comes without values, with a note for the owner, and the agent asks for them if it lacks them.
func TestUnacknowledgedReleasesStopAtTheirLimit(t *testing.T) {
	r := newReleaseFixture(t, true)
	ctx := context.Background()
	for attempt := range 5 {
		c := mustPoll(t, r.f, r.identity, r.op, true)
		if c.SecretRelease == nil || !c.SecretRelease.Values {
			t.Fatalf("attempt %d: %+v", attempt, c.SecretRelease)
		}
		if _, err := r.f.st.Pool.Exec(ctx, "UPDATE secret_releases SET last_attempt_at=now()-interval '1 minute' WHERE operation_id=$1", r.op); err != nil {
			t.Fatal(err)
		}
	}
	capped := mustPoll(t, r.f, r.identity, r.op, true)
	if capped.SecretRelease == nil || capped.SecretRelease.Values || secretOf(t, capped, r.secret) != "" {
		t.Fatalf("released past the limit: %+v", capped.SecretRelease)
	}
	var note string
	if err := r.f.st.Pool.QueryRow(ctx, "SELECT note FROM execution_operations WHERE id=$1", r.op).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if got := r.records(t); len(got) != 5 || got[1] != "unacknowledged" || note == "" {
		t.Fatalf("records %v, note %q", got, note)
	}
	// An agent that lacks them asks again, past the limit.
	if err := r.f.execution.NoticeSecretReleases(ctx, r.identity, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: r.op, Epoch: capped.SecretRelease.Epoch, Reason: api.JournalMissing}}}); err != nil {
		t.Fatal(err)
	}
	again := mustPoll(t, r.f, r.identity, r.op, true)
	if again.SecretRelease == nil || !again.SecretRelease.Values || secretOf(t, again, r.secret) != "release-canary" {
		t.Fatalf("re-delivery past the limit: %+v", again.SecretRelease)
	}
	if got := r.records(t); len(got) != 6 || got[5] != "redelivery" {
		t.Fatalf("records after re-delivery: %v", got)
	}
}

// An agent that does not acknowledge releases gets no values, and the operation says why
// beside what it already says. A stop still reaches it.
func TestAgentsThatDoNotAcknowledgeGetNoValues(t *testing.T) {
	r := newReleaseFixture(t, false)
	ctx := context.Background()
	if _, err := r.f.st.Pool.Exec(ctx, "UPDATE execution_operations SET note='Earlier note.' WHERE id=$1", r.op); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if c := pollOnce(t, r.f, r.identity, 1500*time.Millisecond); c != nil && c.Id == r.op {
			t.Fatalf("an agent without the capability received the command: %+v", c.SecretRelease)
		}
	}
	var note string
	if err := r.f.st.Pool.QueryRow(ctx, "SELECT note FROM execution_operations WHERE id=$1", r.op).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note, "Earlier note. ") || strings.Count(note, "upgraded") != 1 || len(r.records(t)) != 0 {
		t.Fatalf("note %q, records %v", note, r.records(t))
	}
	r.b.call("POST", r.prefix+"/operations/"+r.op.String()+"/cancel", nil, 204, nil)
	stop := mustPoll(t, r.f, r.identity, r.op, true)
	if !stop.CancelRequested || stop.SecretRelease != nil || secretOf(t, stop, r.secret) != "" || len(r.records(t)) != 0 {
		t.Fatalf("stop: %+v, records %v", stop.SecretRelease, r.records(t))
	}
}

// A recovery that dispatches an operation again starts a new epoch, so the host gets its values
// again, recorded as a first release of that epoch.
func TestANewDispatchEpochReleasesAgain(t *testing.T) {
	r := newReleaseFixture(t, true)
	ctx := context.Background()
	first := mustPoll(t, r.f, r.identity, r.op, true)
	if err := acknowledgeRelease(ctx, r.f.execution, r.identity, first); err != nil {
		t.Fatal(err)
	}
	if _, err := r.f.st.Pool.Exec(ctx, "UPDATE execution_operations SET dispatch_epoch=dispatch_epoch+1 WHERE id=$1", r.op); err != nil {
		t.Fatal(err)
	}
	next := mustPoll(t, r.f, r.identity, r.op, true)
	if next.SecretRelease == nil || !next.SecretRelease.Values || next.SecretRelease.Epoch != first.SecretRelease.Epoch+1 {
		t.Fatalf("new epoch: %+v", next.SecretRelease)
	}
	if got := r.records(t); len(got) != 2 || got[1] != "first-release" {
		t.Fatalf("records: %v", got)
	}
}
