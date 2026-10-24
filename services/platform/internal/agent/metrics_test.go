package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestHostMetricParsing(t *testing.T) {
	counters, ok := hostCPU("cpu 10 20 30 40 50 60 70 80 900 1000\ncpu0 1 2 3 4")
	if !ok || counters.total != 360 || counters.idle != 90 {
		t.Fatalf("guest time counted twice or idle miscounted: %+v", counters)
	}
	for _, raw := range []string{"cpu0 1 2 3 4", "cpu 1 nope 3 4", "cpu 18446744073709551615 1 0 0"} {
		if _, ok := hostCPU(raw); ok {
			t.Fatalf("accepted malformed CPU counters %q", raw)
		}
	}
	memory := hostMemory("MemTotal: 1024 kB\nMemFree: 10 kB\nMemAvailable: 512 kB\n")
	if memory == nil || memory.UsedBytes != 512*1024 || memory.TotalBytes != 1024*1024 {
		t.Fatalf("available memory not respected: %+v", memory)
	}
	for _, raw := range []string{"MemTotal: 1024 kB", "MemTotal: 100 kB\nMemAvailable: 101 kB", "MemTotal: 10 kB\nMemAvailable: -1 kB"} {
		if hostMemory(raw) != nil {
			t.Fatal("missing/invalid memory became a zero reading")
		}
	}
}

func TestMetricCollectionIsReadOnlyAndBoundsSlowDocker(t *testing.T) {
	server, resource, unit := uuid.New(), uuid.New(), uuid.New()
	var slow atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("collector attempted mutation: %s", r.Method)
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/info"):
			fmt.Fprintf(w, `{"DockerRootDir":%q}`, t.TempDir())
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			if r.URL.Query().Get("limit") != "41" || !strings.Contains(r.URL.Query().Get("filters"), server.String()) {
				t.Error("unbounded or unowned inventory")
			}
			fmt.Fprintf(w, `[{"Id":"test-container","State":"running","Labels":{"com.astilba.resource":%q,"com.astilba.unit":%q}}]`, resource, unit)
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprint(w, `{"State":{"Health":{"Status":"unhealthy"}},"Config":{"Env":["PRIVATE-METRICS-CANARY"]}}`)
		case strings.HasSuffix(r.URL.Path, "/stats"):
			if slow.Load() {
				<-r.Context().Done()
				return
			}
			// Missing memory statistics must not be interpreted as zero usage.
			fmt.Fprint(w, `{"cpu_stats":{"cpu_usage":{"total_usage":10},"system_cpu_usage":100,"online_cpus":2}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer daemon.Close()
	docker, e := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if e != nil {
		t.Fatal(e)
	}
	defer docker.Close()
	collector := &MetricCollector{}
	sample := collector.Collect(context.Background(), docker, server, t.TempDir())
	if !sample.Partial || len(sample.Services) != 1 || sample.Services[0].MemoryUsedBytes != nil {
		t.Fatalf("missing stats looked complete: %+v", sample)
	}
	if sample.Services[0].Health == nil || *sample.Services[0].Health != "unhealthy" {
		t.Fatal("health summary missing from older-daemon inspect path")
	}
	raw, _ := json.Marshal(sample)
	if strings.Contains(string(raw), "PRIVATE-METRICS-CANARY") {
		t.Fatal("inspect configuration escaped into metrics")
	}
	slow.Store(true)
	started := time.Now()
	sample = collector.Collect(context.Background(), docker, server, t.TempDir())
	if !sample.Partial || time.Since(started) > 2*time.Second {
		t.Fatal("slow stats blocked bounded collection")
	}
}
