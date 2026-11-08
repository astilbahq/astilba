package integration

import (
	"encoding/json/v2"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// replace asks for a plan that moves the environment's primary to the server, naming the volumes
// the owner accepts starting empty.
func (g *revokedGroup) replace(e *primaryEnvironment, server uuid.UUID, volumes ...uuid.UUID) reply {
	request := api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: server, AllowHostChanges: true}
	if volumes != nil {
		request.Replacement = &api.PrimaryReplacementRequest{AbandonedVolumeIds: volumes}
	}
	return <-g.b.send("POST", e.path+"/deployment-plans", request)
}

func wantRefusedField(t *testing.T, got reply, field string) {
	t.Helper()
	var problem struct {
		Fields []string `json:"fields"`
	}
	if got.err != nil || got.status != http.StatusBadRequest || json.Unmarshal(got.body, &problem) != nil || !slices.Contains(problem.Fields, field) {
		t.Fatalf("plan = %d %s %v, want refused on %s", got.status, got.body, got.err, field)
	}
}

func (g *revokedGroup) unitOf(t *testing.T, e *primaryEnvironment, primary bool) (unit uuid.UUID, server uuid.UUID) {
	t.Helper()
	environment := uuid.MustParse(e.path[strings.LastIndex(e.path, "/")+1:])
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT id,server_id FROM deployment_units WHERE environment_id=$1 AND primary_unit=$2`, environment, primary).Scan(&unit, &server); err != nil {
		t.Fatal(err)
	}
	return unit, server
}

// A revoked primary holds the environment's data, so moving off it says what is left behind and
// needs the owner to accept exactly that before anything is planned.
func TestReplacingARevokedPrimaryStartsItsVolumesEmpty(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	g.revoke(t, gone)
	wantRefusedField(t, g.replace(&e, b.id), "replacement")
	withoutHostChanges := <-g.b.send("POST", e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: b.id, Replacement: &api.PrimaryReplacementRequest{AbandonedVolumeIds: []uuid.UUID{volume}}})
	wantRefusedField(t, withoutHostChanges, "allowHostChanges")
	wantRefusedField(t, g.replace(&e, b.id, uuid.New()), "replacement.abandonedVolumeIds")
	wantRefusedField(t, g.replace(&e, b.id, []uuid.UUID{}...), "replacement.abandonedVolumeIds")
	status := g.applied(t, &e, g.replace(&e, b.id, volume))
	if status.Plan.Replacement == nil || status.Plan.Replacement.Server != gone.id || !slices.Equal(status.Plan.Replacement.AbandonedVolumes, []uuid.UUID{volume}) {
		t.Fatalf("the plan doesn't record what it replaces: %+v", status.Plan.Replacement)
	}
	if !slices.ContainsFunc(status.Plan.Effects, func(effect string) bool { return strings.Contains(effect, "Start without the data in Files") }) {
		t.Fatalf("the review doesn't say what's left behind: %q", status.Plan.Effects)
	}
	if _, server := g.unitOf(t, &e, true); server != b.id {
		t.Fatal("the environment didn't move to the new primary")
	}
	var runtimeServer uuid.UUID
	var claims, kept int
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT (SELECT server_id FROM visual_runtime WHERE plan_id=$1),(SELECT count(*) FROM route_claims WHERE server_id=$2),(SELECT count(*) FROM managed_volumes WHERE id=$3 AND NOT deleted)`, status.ID, gone.id, volume).Scan(&runtimeServer, &claims, &kept); err != nil {
		t.Fatal(err)
	}
	if runtimeServer != b.id || claims != 0 || kept != 1 {
		t.Fatalf("runtime on %s, %d claims left on the revoked server, %d volumes kept", runtimeServer, claims, kept)
	}
	// Once moved, the environment is planned like any other.
	g.applied(t, &e, <-g.b.send("POST", e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: b.id, AllowHostChanges: true}))
}

// The common two-host case: the secondary that's left takes over, keeping its single unit, which
// gains the primary role, the volumes and the ingress.
func TestSecondaryTakesOverFromARevokedPrimary(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	g.applied(t, &e, g.place(t, &e, gone.id, b.id))
	secondary, _ := g.unitOf(t, &e, false)
	former, _ := g.unitOf(t, &e, true)
	g.revoke(t, gone)
	g.networkReady(t)
	wantRefusedField(t, g.place(t, &e, b.id, b.id), "replacement")
	status := g.applied(t, &e, g.replace(&e, b.id, volume))
	if status.Plan.UnitID != secondary {
		t.Fatal("the new primary isn't the unit already on that host")
	}
	for _, unit := range status.Plan.Distributed.Units {
		if unit.Plan.ServerID == gone.id {
			t.Fatal("the revoked primary still takes part")
		}
		if unit.Plan.ServerID == b.id && (!unit.Plan.Local.Primary || unit.Previous.UnitID != secondary || unit.Plan.Local.PrepareIngress) {
			t.Fatal("the host's own unit didn't take the primary role over its previous placement, with the ingress it has")
		}
	}
	if unit, server := g.unitOf(t, &e, true); unit != secondary || server != b.id {
		t.Fatal("the secondary's unit isn't the primary")
	}
	var demoted bool
	var volumeUnit uuid.UUID
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT NOT primary_unit,(SELECT unit_id FROM managed_volumes WHERE id=$2) FROM deployment_units WHERE id=$1`, former, volume).Scan(&demoted, &volumeUnit); err != nil {
		t.Fatal(err)
	}
	if !demoted || volumeUnit != secondary {
		t.Fatal("the revoked primary's unit kept its role or its volumes")
	}
}

// An attempt that doesn't finish leaves the environment on its way to that server, so it can be
// tried again there, and only there.
func TestReplacementCanBeRetriedOnTheSameServer(t *testing.T) {
	g := newRevokedGroup(t, 3)
	gone, b, c := g.hosts[0], g.hosts[1], g.hosts[2]
	e, volume := g.filesStack(t, gone)
	g.revoke(t, gone)
	g.mu.Lock()
	g.failing["visual-deploy/"+b.id.String()] = true
	g.mu.Unlock()
	status := g.reviewed(t, &e, g.replace(&e, b.id, volume))
	if state := g.finished(t, g.apply(t, &e, status).Id); state != "failed" {
		t.Fatalf("the failing attempt ended %s", state)
	}
	g.mu.Lock()
	delete(g.failing, "visual-deploy/"+b.id.String())
	g.mu.Unlock()
	var view api.PrimaryReplacementView
	g.b.call("GET", e.path+"/primary-replacement", nil, 200, &view)
	if !view.Required || view.RevokedServerId == nil || *view.RevokedServerId != gone.id || view.ContinueOnServerId == nil || *view.ContinueOnServerId != b.id || !slices.Equal(view.AbandonedVolumeIds, []uuid.UUID{volume}) {
		t.Fatalf("the dashboard isn't told how to continue: %+v", view)
	}
	var runtimeServer uuid.UUID
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT server_id FROM visual_runtime WHERE environment_id=(SELECT environment_id FROM deployment_units WHERE server_id=$1 LIMIT 1)`, b.id).Scan(&runtimeServer); err != nil || runtimeServer != gone.id {
		t.Fatalf("a failed attempt moved the runtime record to %s: %v", runtimeServer, err)
	}
	wantRefusedField(t, g.replace(&e, c.id, volume), "serverId")
	g.applied(t, &e, g.replace(&e, b.id, volume))
	g.b.call("GET", e.path+"/primary-replacement", nil, 200, &view)
	if view.Required {
		t.Fatal("a finished replacement still asks for one")
	}
}

// A reviewed replacement relied on the revocation. Renewing the server first means it could
// answer again with its data, so the plan stops.
func TestReplacementNeedsThePrimaryStillRevoked(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	g.revoke(t, gone)
	status := g.reviewed(t, &e, g.replace(&e, b.id, volume))
	g.b.call("POST", g.base+"/servers/"+gone.id.String()+"/enrollment", nil, 201, nil)
	g.b.call("POST", e.path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 409, nil)
	// A healthy primary needs no replacement and can't be given one.
	wantRefusedField(t, g.replace(&e, b.id, volume), "replacement")
}

// A host that has only run secondaries has no dedicated ingress, which a primary needs to reach
// the secondaries left. Its agent prepares one during reserve, which restarts its proxy, so the
// plan names whose traffic drops. A host whose agent can't is refused before anything starts.
func TestReplacementPreparesTheNewPrimarysIngress(t *testing.T) {
	g := newRevokedGroup(t, 3)
	gone, b, c := g.hosts[0], g.hosts[1], g.hosts[2]
	e, volume := g.filesStack(t, gone)
	e.spec.Applications[0].Replicas = 2
	g.applied(t, &e, g.place(t, &e, gone.id, b.id, c.id))
	g.revoke(t, gone)
	g.networkReady(t)
	no, yes := false, true
	g.reports(t, b, func(_ *api.AgentCapabilities, preflight *api.HostPreflight) {
		inventory := *preflight.CrossHostInventory
		inventory.IngressReady = &no
		preflight.CrossHostInventory = &inventory
	})
	wantRefusedField(t, g.place(t, &e, b.id, b.id, c.id), "replacement")
	wantRefusedField(t, g.replace(&e, b.id, volume), "placement.preparePrimaryIngress")
	g.reports(t, b, func(capabilities *api.AgentCapabilities, _ *api.HostPreflight) { capabilities.PrimaryIngress = &yes })
	var project api.Project
	g.b.call("POST", g.base+"/projects", map[string]string{"name": "Neighbours"}, 201, &project)
	var neighbour api.Environment
	g.b.call("POST", g.base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Shop"}, 201, &neighbour)
	if _, err := g.f.st.Pool.Exec(g.ctx, `INSERT INTO route_claims(server_id,claim,workspace_id,environment_id) VALUES($1,'shop.example.com',$2,$3)`, b.id, g.ws, neighbour.Id); err != nil {
		t.Fatal(err)
	}
	status := g.applied(t, &e, g.replace(&e, b.id, volume))
	for _, unit := range status.Plan.Distributed.Units {
		if unit.Plan.Local.PrepareIngress != (unit.Plan.ServerID == b.id) {
			t.Fatalf("server %s prepares ingress: %v", unit.Plan.ServerID, unit.Plan.Local.PrepareIngress)
		}
	}
	if !slices.ContainsFunc(status.Plan.Effects, func(effect string) bool { return strings.Contains(effect, "Traffic to Neighbours / Shop drops") }) {
		t.Fatalf("the plan doesn't name whose traffic drops: %q", status.Plan.Effects)
	}
}

// A primary whose proxy reaches no other host needs no dedicated ingress, so its proxy is left as
// it is.
func TestReplacementLeavesALoneIngressAlone(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	g.applied(t, &e, g.place(t, &e, gone.id, b.id))
	g.revoke(t, gone)
	g.networkReady(t)
	no := false
	g.reports(t, b, func(_ *api.AgentCapabilities, preflight *api.HostPreflight) {
		inventory := *preflight.CrossHostInventory
		inventory.IngressReady = &no
		preflight.CrossHostInventory = &inventory
	})
	wantRefusedField(t, g.place(t, &e, b.id, b.id), "replacement")
	status := g.applied(t, &e, g.replace(&e, b.id, volume))
	if status.Plan.Distributed.Units[0].Plan.Local.PrepareIngress {
		t.Fatal("a lone primary prepares ingress it doesn't need")
	}
}

// If the server an unfinished attempt chose is revoked as well, nothing it started can be continued,
// so the environment can move again rather than wait on either revoked server.
func TestReplacementMovesOnWhenItsTargetIsRevokedToo(t *testing.T) {
	g := newRevokedGroup(t, 3)
	gone, b, c := g.hosts[0], g.hosts[1], g.hosts[2]
	e, volume := g.filesStack(t, gone)
	g.revoke(t, gone)
	g.mu.Lock()
	g.failing["visual-deploy/"+b.id.String()] = true
	g.mu.Unlock()
	status := g.reviewed(t, &e, g.replace(&e, b.id, volume))
	if state := g.finished(t, g.apply(t, &e, status).Id); state != "failed" {
		t.Fatalf("the failing attempt ended %s", state)
	}
	g.revoke(t, b)
	g.applied(t, &e, g.replace(&e, c.id, volume))
	if _, server := g.unitOf(t, &e, true); server != c.id {
		t.Fatal("the environment didn't move to the healthy server")
	}
}

// A volume can be declared without being mounted, and it still has to exist on the new primary.
func TestReplacementKeepsADeclaredVolumeNothingMounts(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	spare := uuid.New()
	e := g.deployed(t, gone, func(string) domain.Specification {
		app := webApplication(uuid.New())
		return domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: spare, Name: "Spare"}}, Applications: []domain.Application{app}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app.ID, Protocol: "http", Domain: "spare.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	})
	g.revoke(t, gone)
	g.applied(t, &e, g.replace(&e, b.id, spare))
	var deleted bool
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT deleted FROM managed_volumes WHERE id=$1`, spare).Scan(&deleted); err != nil || deleted {
		t.Fatalf("the declared volume was dropped: %v %v", deleted, err)
	}
}

// A server can still hold the environment's unit from an earlier placement. Taking over keeps that
// one unit, rather than adding the primary beside it.
func TestReplacementOntoAFormerSecondaryKeepsOneUnit(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	// What consolidating a placement leaves behind: the server keeps the environment's unit row.
	former := uuid.New()
	environment := uuid.MustParse(e.path[strings.LastIndex(e.path, "/")+1:])
	if _, err := g.f.st.Pool.Exec(g.ctx, `INSERT INTO deployment_units(workspace_id,id,environment_id,server_id,primary_unit) VALUES($1,$2,$3,$4,false)`, g.ws, former, environment, b.id); err != nil {
		t.Fatal(err)
	}
	g.revoke(t, gone)
	status := g.applied(t, &e, g.replace(&e, b.id, volume))
	var units int
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT count(*) FROM deployment_units WHERE server_id=$1`, b.id).Scan(&units); err != nil || units != 1 || status.Plan.UnitID != former {
		t.Fatalf("%d units on the new primary, plan unit %s, former secondary %s: %v", units, status.Plan.UnitID, former, err)
	}
}

// A replacement reviewed before another one succeeded no longer applies: the environment has moved,
// and applying it would detach the healthy primary it moved to.
func TestStaleReplacementDoesNotUndoANewerOne(t *testing.T) {
	g := newRevokedGroup(t, 3)
	gone, b, c := g.hosts[0], g.hosts[1], g.hosts[2]
	e, volume := g.filesStack(t, gone)
	g.revoke(t, gone)
	stale := g.reviewed(t, &e, g.replace(&e, b.id, volume))
	g.applied(t, &e, g.replace(&e, c.id, volume))
	g.b.call("POST", e.path+"/deployment-plans/"+stale.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 409, nil)
	if _, server := g.unitOf(t, &e, true); server != c.id {
		t.Fatal("a stale replacement moved the environment again")
	}
}
