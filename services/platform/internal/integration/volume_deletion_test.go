package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// volumeEnvironment is an environment whose one web service mounted a volume, deployed on a server
// the test answers for. Its draft and the deployment it runs declare the volume but no longer
// mount it, so the volume can be deleted.
type volumeEnvironment struct {
	t               *testing.T
	f               *fixture
	b               *browser
	ws, env, server uuid.UUID
	token, path     string
	app, volume     uuid.UUID
	draft           api.Revision
}

func newVolumeEnvironment(t *testing.T) *volumeEnvironment {
	f := setup(t)
	b := f.browser(t)
	e := &volumeEnvironment{t: t, f: f, b: b, app: uuid.New(), volume: uuid.New()}
	e.ws = b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + e.ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Volumes"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	e.env, e.path = env.Id, prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()
	enrolled, in, token := enrollment(t, f, b, e.ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	e.server, e.token = enrolled.Server.Id, token
	b.call("PUT", e.path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": e.spec(true)}, 200, &e.draft)
	e.deploy()
	e.draft = e.save(false)
	e.deploy()
	return e
}

// spec is the environment's specification, with the service mounting the volume or not.
func (e *volumeEnvironment) spec(mounted bool) domain.Specification {
	image := execution.TestImage
	app := domain.Application{ID: e.app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}
	if mounted {
		app.Mounts = []domain.VolumeMount{{VolumeID: e.volume, Target: "/data"}}
	}
	return domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{app}, Volumes: []domain.Volume{{ID: e.volume, Name: "Data"}}}
}

// save makes the draft mount the volume or not, as the dashboard does, and fails unless it's saved.
func (e *volumeEnvironment) save(mounted bool) api.Revision {
	e.t.Helper()
	var out api.Revision
	e.b.call("PUT", e.path+"/draft", map[string]any{"baseRevisionId": e.draft.Id, "specification": e.spec(mounted)}, 200, &out)
	return out
}

// saving sends a save of the draft that mounts the volume, for the test to judge.
func (e *volumeEnvironment) saving() <-chan reply {
	return e.b.send("PUT", e.path+"/draft", map[string]any{"baseRevisionId": e.draft.Id, "specification": e.spec(true)})
}

// deleting sends a delete of the volume, for the test to judge.
func (e *volumeEnvironment) deleting() <-chan reply {
	return e.b.send("DELETE", e.path+"/volumes/"+e.volume.String(), nil)
}

// poll waits for the server to be sent operation id.
func (e *volumeEnvironment) poll(id uuid.UUID) api.ExecutionCommand {
	e.t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		var p api.AgentPoll
		agentRequest(e.t, e.f.server.Client(), e.f.server.URL, "/agent/v1/poll", e.token, nil, 200, &p)
		if p.Command != nil && p.Command.Id == id {
			return *p.Command
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("operation not dispatched")
	return api.ExecutionCommand{}
}

// report ends operation id as state, as the server would.
func (e *volumeEnvironment) report(id uuid.UUID, state api.ExecutionState, obs deployment.Observation) {
	e.t.Helper()
	c := e.poll(id)
	obs.ObservedAt, obs.StateHash = time.Now(), strings.Repeat("a", 64)
	if obs.Resources == nil {
		obs.Resources = []deployment.Resource{}
	}
	raw, _ := stablejson.Marshal(obs)
	agentRequest(e.t, e.f.server.Client(), e.f.server.URL, "/agent/v1/report", e.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: state, Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
}

// planning sends a request for a plan of the current draft.
func (e *volumeEnvironment) planning() reply {
	return <-e.b.send("POST", e.path+"/deployment-plans", api.VisualPlanRequest{ServerId: e.server, BaseRevisionId: e.draft.Id})
}

// plan prepares a plan of the current draft, resolved by the server.
func (e *volumeEnvironment) plan() deployment.Status {
	e.t.Helper()
	var s deployment.Status
	if json.Unmarshal(requireReply(e.t, e.planning(), http.StatusCreated), &s) != nil {
		e.t.Fatal("invalid plan reply")
	}
	e.report(s.OperationID, "succeeded", deployment.Observation{Images: map[string]string{e.app.String(): execution.TestImage}})
	return s
}

// applying sends an apply of a prepared plan.
func (e *volumeEnvironment) applying(plan deployment.Status) reply {
	return <-e.b.send("POST", e.path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()})
}

// deploy prepares and applies a plan of the current draft, which the server runs.
func (e *volumeEnvironment) deploy() {
	e.t.Helper()
	var op api.Operation
	if json.Unmarshal(requireReply(e.t, e.applying(e.plan()), http.StatusCreated), &op) != nil {
		e.t.Fatal("invalid apply reply")
	}
	e.report(op.Id, "succeeded", deployment.Observation{Resources: []deployment.Resource{{ID: e.app, Kind: "service", State: "running", Image: execution.TestImage}}, Applied: true})
}

// deleted starts a delete of the volume and returns its operation.
func (e *volumeEnvironment) deleted() uuid.UUID {
	e.t.Helper()
	var op api.Operation
	if json.Unmarshal(requireReply(e.t, <-e.deleting(), http.StatusCreated), &op) != nil {
		e.t.Fatal("invalid delete reply")
	}
	return op.Id
}

// A draft can't mount a volume a delete hasn't finished with, nor a plan be prepared or applied
// that uses it, and the volume can't be deleted twice at once. A delete that fails keeps the
// volume usable; once one succeeds, nothing can use the volume again.
func TestADraftCantUseAVolumeBeingDeleted(t *testing.T) {
	e := newVolumeEnvironment(t)
	// A plan prepared before the delete still declares the volume.
	prepared := e.plan()
	deletion := e.deleted()
	refused(t, "a draft mounting a volume being deleted", <-e.saving(), "volume.deleting")
	refused(t, "a second delete", <-e.deleting(), "volume.deleting")
	refused(t, "applying a plan prepared before the delete", e.applying(prepared), "volume.deleting")
	refused(t, "a plan of a draft that declares the volume", e.planning(), "volume.deleting")

	// A failed delete leaves the volume as it was.
	e.report(deletion, "failed", deployment.Observation{})
	e.draft = e.save(true)
	e.draft = e.save(false)

	prepared = e.plan()
	deletion = e.deleted()
	e.report(deletion, "succeeded", deployment.Observation{})
	refused(t, "a draft mounting a deleted volume", <-e.saving(), "volume.deleted")
	refused(t, "a delete of a deleted volume", <-e.deleting(), "volume.deleted")
	refused(t, "applying a plan that declares a deleted volume", e.applying(prepared), "volume.deleted")
	refused(t, "a plan of a draft that declares a deleted volume", e.planning(), "volume.deleted")
}

// A volume the draft mounts, saved but not yet deployed, can't be deleted.
func TestAVolumeAPendingDraftMountsIsntDeleted(t *testing.T) {
	e := newVolumeEnvironment(t)
	e.draft = e.save(true)
	refused(t, "deleting a volume the draft mounts", <-e.deleting(), "volume.inUse")
	var volumes []api.ManagedVolume
	e.b.call("GET", e.path+"/volumes", nil, 200, &volumes)
	if len(volumes) != 1 || volumes[0].Deleted || !volumes[0].InUse {
		t.Fatalf("volumes after a refused delete = %+v", volumes)
	}
}

// Of a draft that mounts a volume and a delete of it, sent together, exactly one goes ahead. That
// holds too for a draft written without the workspace lock, as a pipeline's report writes one.
func TestADraftAndADeleteOfItsVolumeDontBothGoAhead(t *testing.T) {
	e := newVolumeEnvironment(t)
	saving, deleting := e.saving(), e.deleting()
	saved, deleted := <-saving, <-deleting
	switch {
	case saved.status == http.StatusOK:
		refused(t, "a delete beside a saved draft", deleted, "volume.inUse")
		if json.Unmarshal(saved.body, &e.draft) != nil {
			t.Fatal("invalid draft reply")
		}
		e.draft = e.save(false)
	case deleted.status == http.StatusCreated:
		refused(t, "a draft beside a delete", saved, "volume.deleting")
		var op api.Operation
		if json.Unmarshal(deleted.body, &op) != nil {
			t.Fatal("invalid delete reply")
		}
		e.report(op.Id, "failed", deployment.Observation{})
	default:
		t.Fatalf("a draft and a delete together = %d %s and %d %s", saved.status, saved.body, deleted.status, deleted.body)
	}

	// The draft is written first and holds the environment while the delete waits for it.
	ctx := context.Background()
	tx, err := e.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	raw, _ := stablejson.Marshal(e.spec(true))
	spec, canonical, err := domain.ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RefuseDeletedMounts(ctx, q, e.ws, e.env, spec); err != nil {
		t.Fatal(err)
	}
	deleting = e.deleting()
	waitForLockWaiter(t, e.f)
	hash := sha256.Sum256(canonical)
	revision := uuid.New()
	if _, err = store.InsertRevision(ctx, q, db.InsertRevisionParams{WorkspaceID: e.ws, EnvironmentID: e.env, ID: revision, AuthorID: e.draft.AuthorId, Specification: canonical, ContentHash: hash[:]}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.AdvanceDraft(ctx, db.AdvanceDraftParams{WorkspaceID: e.ws, ID: e.env, DraftRevisionID: e.draft.Id, DraftRevisionID_2: revision}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	refused(t, "a delete that waited for a draft mounting the volume", <-deleting, "volume.inUse")
}

// waitForLockWaiter waits until a transaction is waiting for a row lock another holds.
func waitForLockWaiter(t *testing.T, f *fixture) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		var waiting int
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nothing waited for the lock")
}
