package integration

import (
	"testing"
)

// A publication that reaches the primary after its deadline doesn't run, because its probes are
// that old, and it changes nothing. Nothing can issue it again, so the deployment removes its
// candidates and releases every host rather than holding them for a reconciliation with nothing to
// find, and the owner can apply again.
func TestLatePublicationAbortsAndReleasesTheHosts(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	g.arriveLate(a, "publish", true)
	op := g.apply(t, &e, g.reviewed(t, &e, g.place(t, &e, a.id, b.id)))
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a deployment whose publication arrived late ended %s", state)
	}
	if !g.received(a, "abort") || !g.received(b, "abort") || g.received(a, "retire") {
		t.Fatal("the hosts were not told to remove their candidates")
	}
	if g.reservations(t) != 0 {
		t.Fatal("the late publication stranded the reservations")
	}
	g.arriveLate(a, "publish", false)
	g.networkReady(t)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
}

// A file service that never left the primary keeps the identity it had before the placement. Once
// the stack is consolidated, removing the placement keeps that identity, so the environment returns
// to one host, and no cross-host runtime is left to refuse its retirement.
func TestRemovingPlacementAfterConsolidatingKeepsAnUnmovedFileService(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e, _ := g.filesStack(t, a)
	files := e.spec.Applications[1].ID
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	g.applied(t, &e, g.place(t, &e, a.id, a.id))
	single := g.removePlacement(t, &e, a)
	if single.Plan.Local != nil || single.Plan.Instance(files) != files {
		t.Fatal("removing the placement replaced the file service")
	}
	var remaining int
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT count(*) FROM cross_host_runtime WHERE workspace_id=$1`, g.ws).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("%d cross-host runtime rows outlived the placement: %v", remaining, err)
	}
	if g.reservations(t) != 0 {
		t.Fatal("removing the placement kept a reservation")
	}
}
