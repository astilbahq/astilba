package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
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
		{Servers: []routedServer{{Service: "unit-route@file", URL: "http://app.unit:8080"}, {Service: "unit-route@file", URL: "http://10.251.0.2:20000"}}},
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
	if err = (&Engine{Journal: j}).confirmRouted(context.Background(), s, plan); err != nil {
		t.Fatal(err)
	}
}

// Completion waits for a server only if the proxy routed it at reservation, and not for one
// of this host's instances that completion excuses from being ready.
func TestCompletionWaitsOnlyForRecordedServers(t *testing.T) {
	pool := routePool{domain: "a.example.test", service: "unit-a@file", servers: []poolServer{
		{url: "http://ready.unit:8080", probe: "http://ready.unit:8080/health"},
		{url: "http://excused.unit:8080", probe: "http://excused.unit:8080/health"},
		{url: "http://10.251.0.2:20000", probe: "http://10.251.0.2:20000/health"},
		{url: "http://10.251.0.3:20000", probe: "http://10.251.0.3:20000/health"},
	}}
	other := routePool{domain: "b.example.test", service: "unit-b@file", servers: []poolServer{{url: "http://10.251.0.3:20000", probe: "http://10.251.0.3:20000/"}}}
	recorded := []routedServer{
		{Service: "unit-a@file", URL: "http://ready.unit:8080"},
		{Service: "unit-a@file", URL: "http://excused.unit:8080"},
		{Service: "unit-a@file", URL: "http://10.251.0.2:20000"},
		// The same server in another pool is a different server to the proxy.
		{Service: "unit-c@file", URL: "http://10.251.0.3:20000"},
	}
	got, err := requiredPools([]routePool{pool, other}, recorded, map[string]bool{"excused.unit": true})
	if err != nil {
		t.Fatal(err)
	}
	want := []routePool{{domain: "a.example.test", service: "unit-a@file", servers: []poolServer{pool.servers[0], pool.servers[2]}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completion waits for %+v, want %+v", got, want)
	}
}
