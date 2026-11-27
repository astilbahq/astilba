package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func reservedWithRoutes(plan deployment.Plan, routed *maintenanceRoutes) crossHostState {
	return crossHostState{ParentID: uuid.New(), GroupHash: strings.Repeat("b", 64), Unit: deployment.DistributedUnit{Plan: plan}, Phase: "reserved", Pause: &writerPause{Serving: map[string]string{}, Writers: map[string]pausedWriter{}}, Routed: routed}
}

// What the proxy routed at reservation must outlast an agent restart before completion,
// and a reservation that recorded no server must not read as one recorded by an agent
// that recorded nothing.
func TestRoutedServersSurviveARestart(t *testing.T) {
	for _, routed := range []*maintenanceRoutes{
		{Servers: []routedServer{{Service: "unit-route@file", Backend: "service-a:8080"}, {Service: "unit-route@file", Backend: "10.251.0.2:20000"}}},
		{Servers: []routedServer{}},
	} {
		j, plan := testVisualJournal(t)
		if err := j.db.Update(func(tx *bolt.Tx) error {
			return j.put(tx, "cross-host", "active", reservedWithRoutes(plan, routed))
		}); err != nil {
			t.Fatal(err)
		}
		j = reopen(t, j)
		got, err := j.crossHost()
		if err != nil {
			t.Fatal(err)
		}
		if got.Routed == nil || !reflect.DeepEqual(got.Routed.Servers, routed.Servers) {
			t.Fatalf("the restarted journal holds %+v, want %+v", got.Routed, routed)
		}
	}
}

// A reservation an earlier agent recorded names no route servers, so its completion waits
// for none, and asks nothing of the proxy.
func TestEarlierReservationWaitsForNoRouteServer(t *testing.T) {
	j, plan := testVisualJournal(t)
	var earlier map[string]any
	if err := j.db.Update(func(tx *bolt.Tx) error {
		if err := j.put(tx, "cross-host", "active", reservedWithRoutes(plan, nil)); err != nil {
			return err
		}
		if err := j.get(tx, "cross-host", "active", &earlier); err != nil {
			return err
		}
		if _, ok := earlier["Routed"]; !ok {
			return nil
		}
		// As an earlier agent wrote it, without the field.
		delete(earlier, "Routed")
		return j.put(tx, "cross-host", "active", earlier)
	}); err != nil {
		t.Fatal(err)
	}
	j = reopen(t, j)
	s, err := j.crossHost()
	if err != nil {
		t.Fatal(err)
	}
	if s.Routed != nil {
		t.Fatalf("an earlier reservation reads as recording %+v", s.Routed)
	}
	if unrouted, err := (&Engine{Journal: j}).confirmRouted(context.Background(), s, plan); err != nil || unrouted != "" {
		t.Fatal(unrouted, err)
	}
}

// Completion waits for a server only if the proxy routed it at reservation, and not for one
// of this host's instances that completion excuses from being ready, whether the route
// file names the unit's services by their bare or their qualified names. It requires this
// host's servers, and tells apart the servers on other hosts, which it does not.
func TestCompletionWaitsOnlyForRecordedServers(t *testing.T) {
	unit := uuid.New()
	network := "." + deployment.NetworkName(unit)
	recorded := []routedServer{
		{Service: "unit-a@file", Backend: "service-ready:8080"},
		{Service: "unit-a@file", Backend: "service-excused:8080"},
		{Service: "unit-a@file", Backend: "10.251.0.2:20000"},
		// The same server in another pool is a different server to the proxy.
		{Service: "unit-c@file", Backend: "10.251.0.3:20000"},
	}
	for _, suffix := range []string{"", network} {
		pool := routePool{domain: "a.example.test", service: "unit-a@file", servers: []poolServer{
			{url: "http://service-ready" + suffix + ":8080", probe: "http://service-ready" + suffix + ":8080/health"},
			{url: "http://service-excused" + suffix + ":8080", probe: "http://service-excused" + suffix + ":8080/health"},
			{url: "http://10.251.0.2:20000", probe: "http://10.251.0.2:20000/health"},
			{url: "http://10.251.0.3:20000", probe: "http://10.251.0.3:20000/health"},
		}}
		other := routePool{domain: "b.example.test", service: "unit-b@file", servers: []poolServer{{url: "http://10.251.0.3:20000", probe: "http://10.251.0.3:20000/"}}}
		local, remote, err := requiredPools(unit, []routePool{pool, other}, recorded, map[string]bool{"service-excused": true})
		if err != nil {
			t.Fatal(err)
		}
		wantLocal := []routePool{{domain: "a.example.test", service: "unit-a@file", servers: []poolServer{pool.servers[0]}}}
		wantRemote := []routePool{{domain: "a.example.test", service: "unit-a@file", servers: []poolServer{pool.servers[2]}}}
		if !reflect.DeepEqual(local, wantLocal) || !reflect.DeepEqual(remote, wantRemote) {
			t.Fatalf("with names ending %q completion requires %+v and waits for %+v, want %+v and %+v", suffix, local, remote, wantLocal, wantRemote)
		}
	}
}

// A pool whose check the proxy refuses is never routed, so maintenance neither records nor
// waits for its servers, but the unit's other pools still count.
func TestRefusedCheckLeavesOtherPoolsWaitedFor(t *testing.T) {
	unit := uuid.New()
	valid, refused := uuid.New(), uuid.New()
	file := `{"http":{"services":{` +
		`"` + deployment.UnitName(unit) + `-` + valid.String() + `":{"loadBalancer":{"servers":[{"url":"http://service-a:8080"}],"healthCheck":{"path":"/health"}}},` +
		`"` + deployment.UnitName(unit) + `-` + refused.String() + `":{"loadBalancer":{"servers":[{"url":"http://service-b:8080"}],"healthCheck":{"path":"//elsewhere/health"}}}}}}`
	s := routeSnapshot{Sets: map[string]deployment.RouteSet{unit.String(): {UnitID: unit, Routes: []domain.Route{{ID: valid, Protocol: "http", Domain: "a.example.test"}, {ID: refused, Protocol: "http", Domain: "b.example.test"}}}}}
	pools, err := maintenancePools(s, []byte(file), unit)
	if err != nil || len(pools) != 1 || pools[0].domain != "a.example.test" {
		t.Fatalf("maintenance waits for %+v: %v", pools, err)
	}
	if _, err = unitPools(s, []byte(file), unit.String()); err == nil {
		t.Fatal("a publish accepted a pool whose check the proxy refuses")
	}
}
