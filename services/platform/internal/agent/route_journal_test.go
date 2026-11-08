package agent

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// headerHeavySet is one environment's routes carrying about 6 KB of response headers
// each, within what an environment may declare.
func headerHeavySet(routes int) deployment.RouteSet {
	unit, environment, app := uuid.New(), uuid.New(), uuid.New()
	value := strings.Repeat("x", 1000)
	set := deployment.RouteSet{UnitID: unit, EnvironmentID: environment, Backends: map[string]deployment.Backend{app.String(): {Address: deployment.ServiceName(app)}}}
	for i := range routes {
		headers := map[string]string{}
		for k := range 6 {
			headers[fmt.Sprintf("X-Header-%d", k)] = value
		}
		set.Routes = append(set.Routes, domain.Route{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: fmt.Sprintf("r%d.%s.example.test", i, unit), Port: 8080, TLS: "off", Advanced: &domain.RouteOptions{ResponseHeaders: headers}})
	}
	return set
}

// routingRecords returns the size of every record in the routing bucket.
func routingRecords(t *testing.T, j *Journal) map[string]int {
	t.Helper()
	sizes := map[string]int{}
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("routing")).ForEach(func(k, _ []byte) error {
			var raw jsontext.Value
			if err := j.get(tx, "routing", string(k), &raw); err != nil {
				return err
			}
			sizes[string(k)] = len(raw)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return sizes
}

func sameRoutes(t *testing.T, got, want routeSnapshot) {
	t.Helper()
	a, err := stablejson.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := stablejson.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("routes changed:\n got %.300s\nwant %.300s", a, b)
	}
}

// Each environment's routes are recorded on their own, so a host's routing records do not
// grow with its environments. When one record held the whole host, three times over while
// an attempt was pending, two such environments stopped every later publish on the host,
// including one for an environment with no routes.
func TestRoutingRecordsDoNotGrowWithTheHost(t *testing.T) {
	j, _ := testVisualJournal(t)
	current := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{}, QualifiedBackends: true}
	for range 8 {
		set := headerHeavySet(15)
		current.Sets[set.UnitID.String()] = set
	}
	if err := j.saveRouting(routeState{Current: current}); err != nil {
		t.Fatal(err)
	}
	// An environment without routes leaves the routes as they are.
	if err := j.saveRouting(routeState{Current: current, Pending: &routeAttempt{Previous: current, Candidate: current}}); err != nil {
		t.Fatalf("an unchanged attempt could not be recorded: %v", err)
	}
	added := headerHeavySet(15)
	candidate := current
	candidate.Sets = map[string]deployment.RouteSet{added.UnitID.String(): added}
	for unit, set := range current.Sets {
		candidate.Sets[unit] = set
	}
	candidate.Revision = uuid.NewString()
	if err := j.saveRouting(routeState{Current: current, Pending: &routeAttempt{Previous: current, Candidate: candidate}}); err != nil {
		t.Fatalf("another environment's routes could not be recorded: %v", err)
	}
	set, err := stablejson.Marshal(added)
	if err != nil {
		t.Fatal(err)
	}
	for key, size := range routingRecords(t, j) {
		if size > len(set)+4096 {
			t.Errorf("record %s holds %d bytes, more than one environment's routes", key, size)
		}
	}
	restored, err := j.routing()
	if err != nil || restored.Pending == nil {
		t.Fatal("the attempt was not recorded", err)
	}
	sameRoutes(t, restored.Current, current)
	sameRoutes(t, restored.Pending.Previous, current)
	sameRoutes(t, restored.Pending.Candidate, candidate)

	// Committing the attempt, then removing an environment, leaves no record behind.
	if err = j.saveRouting(routeState{Current: candidate}); err != nil {
		t.Fatal(err)
	}
	delete(candidate.Sets, added.UnitID.String())
	if err = j.saveRouting(routeState{Current: candidate}); err != nil {
		t.Fatal(err)
	}
	if _, ok := routingRecords(t, j)[routeSetPrefix+added.UnitID.String()]; ok {
		t.Fatal("a removed environment's routes are still recorded")
	}
	if restored, err = j.routing(); err != nil {
		t.Fatal(err)
	}
	sameRoutes(t, restored.Current, candidate)
}

// Every attempt starts from the current routes, so an attempt stored as its changes can
// recover its previous routes only if that holds.
func TestRouteAttemptMustStartFromTheCurrentRoutes(t *testing.T) {
	j, _ := testVisualJournal(t)
	set := headerHeavySet(1)
	current := routeSnapshot{Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{set.UnitID.String(): set}}
	other := routeSnapshot{Ports: []int{}, Sets: map[string]deployment.RouteSet{}}
	if err := j.saveRouting(routeState{Current: current, Pending: &routeAttempt{Previous: other, Candidate: current}}); err == nil {
		t.Fatal("an attempt from other routes was recorded")
	}
}

// legacyRouting stores a host's routes as preceding agents did, in one record.
func legacyRouting(t *testing.T, j *Journal, record any) {
	t.Helper()
	raw, err := stablejson.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "routing", routeHostKey, jsontext.Value(raw)) }); err != nil {
		t.Fatal(err)
	}
}

// A host upgraded from a preceding agent keeps its routes and any attempt in progress,
// in each encoding that agent wrote, and its next save moves it to records per unit.
func TestRoutingUpgradesFromOneRecord(t *testing.T) {
	type object struct {
		Previous  routeSnapshot `json:"previous"`
		Candidate routeSnapshot `json:"candidate"`
	}
	one, two := headerHeavySet(2), headerHeavySet(1)
	app := uuid.New()
	replicas := two
	replicas.Backends = map[string]deployment.Backend{two.Routes[0].TargetID.String(): {InstanceIDs: []uuid.UUID{app, uuid.New()}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
	current := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, ProxyHash: "proxy", Email: "ops@example.test", ManagedEgress: true, Sets: map[string]deployment.RouteSet{one.UnitID.String(): one}}
	candidate := current
	candidate.Revision = uuid.NewString()
	candidate.Sets = map[string]deployment.RouteSet{one.UnitID.String(): one, two.UnitID.String(): replicas}
	for name, record := range map[string]any{
		"settled": map[string]any{"current": current},
		"pending": map[string]any{"current": current, "pending": object{Previous: current, Candidate: candidate}},
		"replica": map[string]any{"current": current, "pending": []routeSnapshot{current, candidate}},
	} {
		t.Run(name, func(t *testing.T) {
			j, _ := testVisualJournal(t)
			legacyRouting(t, j, record)
			state, err := j.routing()
			if err != nil {
				t.Fatal(err)
			}
			sameRoutes(t, state.Current, current)
			if (state.Pending != nil) != (name != "settled") {
				t.Fatal("the attempt in progress was not read")
			}
			if state.Pending != nil {
				sameRoutes(t, state.Pending.Previous, current)
				sameRoutes(t, state.Pending.Candidate, candidate)
				// Recovery settles the attempt before anything else is saved.
				state.Current, state.Pending = state.Pending.Previous, nil
			}
			if err = j.saveRouting(state); err != nil {
				t.Fatal(err)
			}
			if _, ok := routingRecords(t, j)[routeSetPrefix+one.UnitID.String()]; !ok {
				t.Fatal("the save kept one record for the host")
			}
			if state, err = j.routing(); err != nil {
				t.Fatal(err)
			}
			sameRoutes(t, state.Current, current)
		})
	}
}

// smallSet is one environment's single plain route.
func smallSet() deployment.RouteSet {
	set := headerHeavySet(1)
	set.Routes[0].Advanced = nil
	return set
}

// routesPlan publishes set's routes to its own unit.
func routesPlan(set deployment.RouteSet) deployment.Plan {
	p := deployment.Plan{UnitID: set.UnitID, EnvironmentID: set.EnvironmentID, Recipe: deployment.Recipe, HostChanges: true, Specification: domain.Specification{Version: 1, Routes: set.Routes}}
	for id := range set.Backends {
		p.Specification.Applications = append(p.Specification.Applications, domain.Application{ID: uuid.MustParse(id)})
	}
	return p
}

// A host's limits are per environment. Environments using all the routes they may declare
// leave another environment's room as it was, and only the number of environments a host
// routes is shared.
func TestRouteLimitsArePerEnvironment(t *testing.T) {
	e := &Engine{}
	full := routeSnapshot{Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{}}
	for range 3 {
		set := headerHeavySet(50)
		for i := range set.Routes {
			set.Routes[i].Advanced = nil
		}
		full.Sets[set.UnitID.String()] = set
	}
	if _, err := e.candidateRoutes(full, routesPlan(smallSet())); err != nil {
		t.Fatalf("environments with 50 routes each stopped another adding one: %v", err)
	}

	crowded := routeSnapshot{Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{}}
	var existing deployment.RouteSet
	for range maxRoutedUnits {
		existing = smallSet()
		crowded.Sets[existing.UnitID.String()] = existing
	}
	if _, err := e.candidateRoutes(crowded, routesPlan(smallSet())); err == nil || !strings.Contains(err.Error(), "host ingress limit") {
		t.Fatalf("a host routed more environments than it may: %v", err)
	}
	existing.Routes[0].Domain = "changed." + existing.Routes[0].Domain
	if _, err := e.candidateRoutes(crowded, routesPlan(existing)); err != nil {
		t.Fatalf("a routed environment could not change its routes on a full host: %v", err)
	}

	oversized := headerHeavySet(50)
	for i := range oversized.Routes {
		for k := range 6 {
			oversized.Routes[i].Advanced.ResponseHeaders[fmt.Sprintf("X-More-%d", k)] = strings.Repeat("y", 1000)
		}
	}
	if _, err := e.candidateRoutes(full, routesPlan(oversized)); err == nil || !strings.Contains(err.Error(), "exceed what a host can record") {
		t.Fatalf("routes no host can record were accepted: %v", err)
	}
}
