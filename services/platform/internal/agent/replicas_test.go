package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestReplicaObservationsDoNotHideMissingOrWrongImageMembers(t *testing.T) {
	id, a, b := uuid.New(), uuid.New(), uuid.New()
	p := deployment.Plan{Recipe: deployment.ReplicaRecipe, Specification: domain.Specification{Applications: []domain.Application{{ID: id, Replicas: 3}}}, ReplicaInstances: map[string][]uuid.UUID{id.String(): {a, b}}, Images: map[string]string{id.String(): "reviewed-image"}}
	rows := []deployment.Resource{}
	for _, instance := range p.InstanceIDs(id) {
		rows = append(rows, deployment.Resource{ID: id, State: "running", Image: "reviewed-image", MemoryBytes: 100, Instances: []deployment.InstanceState{{InstanceID: instance, State: "running"}}})
	}
	all := aggregateReplicaResources(p, rows, true)
	if len(all) != 1 || all[0].State != "running" || all[0].MemoryBytes != 300 || len(all[0].Instances) != 3 {
		t.Fatal("complete replica group was not observed as one logical resource")
	}
	missing := aggregateReplicaResources(p, rows[:2], true)
	if missing[0].State != "degraded" || !missing[0].Drift || missing[0].Instances[2].State != "missing" {
		t.Fatal("missing secondary was reported as a complete healthy group")
	}
	rows[2].Image = "unreviewed-image"
	changed := aggregateReplicaResources(p, rows, true)
	if changed[0].State != "degraded" || !changed[0].Drift {
		t.Fatal("wrong secondary image was hidden behind the primary's image")
	}
}

func TestReplicaMetricsSumEveryRunningMemberAndWithholdPartialTotals(t *testing.T) {
	id, unit := uuid.New(), uuid.New()
	one, two := int64(100), int64(200)
	firstCPU, secondCPU := 10.0, 25.0
	health := api.MetricNoHealthcheck
	samples := []api.ServiceMetrics{
		{ResourceId: id, UnitId: unit, State: "running", MemoryUsedBytes: &one, CpuPercent: &firstCPU, Health: &health},
		{ResourceId: id, UnitId: unit, State: "running", MemoryUsedBytes: &two, CpuPercent: &secondCPU, Health: &health},
	}
	partial := false
	value := aggregateServiceMetrics(samples, 2, &partial)
	if partial || value.MemoryUsedBytes == nil || *value.MemoryUsedBytes != 300 || value.CpuPercent == nil || *value.CpuPercent != 35 || value.Health == nil || *value.Health != api.MetricNoHealthcheck {
		t.Fatal("logical metrics lost a secondary's usage or changed health meaning")
	}
	value = aggregateServiceMetrics(samples, 3, &partial)
	if !partial || value.MemoryUsedBytes != nil || value.CpuPercent != nil || value.Health == nil || *value.Health != api.MetricUnhealthy {
		t.Fatal("partial collection reported a smaller pool's totals as complete")
	}
	samples[1].State, samples[1].MemoryUsedBytes, samples[1].CpuPercent = "paused", nil, nil
	partial = false
	value = aggregateServiceMetrics(samples, 2, &partial)
	if !partial || value.MemoryUsedBytes != nil {
		t.Fatal("a paused member's unmeasured resident memory became zero usage")
	}
}

func TestReplicaMetricInventoryIncludesSecondariesAndAbsentGroups(t *testing.T) {
	j, p := testVisualJournal(t)
	app := uuid.New()
	image := execution.TestImage
	p.Specification.Applications = []domain.Application{{ID: app, Name: "Web", Replicas: 3, Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
	p.Specification.Routes = []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "metrics.example.test", Port: 8080, TLS: "off"}}
	p.Images[app.String()] = image
	p.PrepareInstances(deployment.Plan{})
	p = p.WithHash()
	if err := p.Validate(true); err != nil {
		t.Fatal(err)
	}
	if err := j.saveUnit(unitState{Plan: p}); err != nil {
		t.Fatal(err)
	}
	var absent atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("metrics mutated Docker")
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/info"):
			fmt.Fprintf(w, `{"DockerRootDir":%q}`, t.TempDir())
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			items := []map[string]any{}
			if !absent.Load() {
				ids := append(p.InstanceIDs(app), uuid.New())
				for i, id := range ids {
					items = append(items, map[string]any{"Id": fmt.Sprintf("member-%d", i), "Names": []string{"/" + deployment.UnitName(id)}, "State": "running", "Labels": map[string]string{labelResource: app.String(), "com.astilba.unit": p.UnitID.String()}})
				}
			}
			_ = json.NewEncoder(w).Encode(items)
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprint(w, `{"State":{"Health":{"Status":"healthy"}}}`)
		case strings.HasSuffix(r.URL.Path, "/stats"):
			parts := strings.Split(r.URL.Path, "/")
			index, _ := strconv.Atoi(strings.TrimPrefix(parts[len(parts)-2], "member-"))
			fmt.Fprintf(w, `{"cpu_stats":{"cpu_usage":{"total_usage":10},"system_cpu_usage":100,"online_cpus":2},"memory_stats":{"usage":%d}}`, 100*(index+1))
		default:
			w.WriteHeader(404)
		}
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	collector := MetricCollector{Journal: j}
	sample := collector.Collect(context.Background(), docker, p.ServerID, t.TempDir())
	if len(sample.Services) != 1 || sample.Services[0].MemoryUsedBytes == nil || *sample.Services[0].MemoryUsedBytes != 600 {
		t.Fatalf("active members or candidate exclusion changed: %+v", sample.Services)
	}
	absent.Store(true)
	sample = collector.Collect(context.Background(), docker, p.ServerID, t.TempDir())
	if !sample.Partial || len(sample.Services) != 1 || sample.Services[0].State != "unknown" || sample.Services[0].MemoryUsedBytes != nil {
		t.Fatalf("absent group disappeared from inventory: %+v", sample.Services)
	}
}

// Pending recovery must refuse an older consumer even before the first replica
// route has been committed; rendering only Previous cannot establish that fence.
func TestPendingReplicaRoutingRejectsLegacyReaderBeforeRecovery(t *testing.T) {
	j, p := testVisualJournal(t)
	app := uuid.New()
	route := domain.Route{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "pending.example.test", Port: 8080, TLS: "off"}
	legacy := routeSnapshot{Sets: map[string]deployment.RouteSet{p.UnitID.String(): {UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, Routes: []domain.Route{route}, Backends: map[string]deployment.Backend{app.String(): {Address: deployment.ServiceName(app)}}}}}
	candidate := routeSnapshot{Sets: map[string]deployment.RouteSet{p.UnitID.String(): {UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, Routes: []domain.Route{route}, Backends: map[string]deployment.Backend{app.String(): {InstanceIDs: []uuid.UUID{app, uuid.New()}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}}}}
	type oldAttempt struct {
		Previous  routeSnapshot `json:"previous"`
		Candidate routeSnapshot `json:"candidate"`
	}
	type oldState struct {
		Current routeSnapshot `json:"current"`
		Pending *oldAttempt   `json:"pending"`
	}
	for _, previous := range []routeSnapshot{{Sets: map[string]deployment.RouteSet{}}, legacy, candidate} {
		state := routeState{Current: previous, Pending: &routeAttempt{Previous: previous, Candidate: candidate}}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var old oldState
		if err = json.Unmarshal(raw, &old); err == nil {
			t.Fatal("older reader accepted pending replica routes")
		}
		if err = j.saveRouting(state); err != nil {
			t.Fatal(err)
		}
		restored, err := j.routing()
		if err != nil || restored.Pending == nil {
			t.Fatal("current reader could not recover pending routes", err)
		}
		for i, snapshot := range []routeSnapshot{restored.Pending.Previous, restored.Pending.Candidate} {
			want := previous
			if i == 1 {
				want = candidate
			}
			actual, e1 := renderSnapshot(snapshot)
			expected, e2 := renderSnapshot(want)
			if e1 != nil || e2 != nil || string(actual) != string(expected) {
				t.Fatal("pending recovery changed runtime routing", e1, e2)
			}
		}
	}
	raw, err := json.Marshal(routeState{Current: legacy, Pending: &routeAttempt{Previous: legacy, Candidate: legacy}})
	var old oldState
	if err != nil || json.Unmarshal(raw, &old) != nil || old.Pending == nil {
		t.Fatal("legacy singleton pending encoding changed", err)
	}
}
