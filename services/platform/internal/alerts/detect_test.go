package alerts

import (
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func TestRecoveryRequiresMatchingFreshEvidence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 3, 30, 0, time.UTC)
	id := uuid.New()
	samples := func() []api.MetricSample {
		out := []api.MetricSample{}
		for i := 2; i >= 0; i-- {
			out = append(out, api.MetricSample{ReceivedAt: now.Truncate(time.Minute).Add(-time.Duration(i) * time.Minute), Report: api.MetricReport{Host: api.HostMetrics{DiskSource: "docker", Disk: &api.MetricUsage{UsedBytes: 95, TotalBytes: 100}}, Services: []api.ServiceMetrics{}}})
		}
		return out
	}
	t.Run("disk source gap and recovery", func(t *testing.T) {
		v := samples()
		if diskEvidence(v, now, "docker") != bad {
			t.Fatal("sustained pressure missing")
		}
		for i := 1; i < 3; i++ {
			v[i].Report.Host.DiskSource = "agent"
			v[i].Report.Host.Disk.UsedBytes = 20
		}
		if diskEvidence(v, now, "docker") != unknown {
			t.Fatal("fallback filesystem cleared Docker pressure")
		}
		if diskEvidence(v, now, "agent") != good {
			t.Fatal("matching low readings did not clear fallback pressure")
		}
		v = samples()
		v[1].ReceivedAt = v[1].ReceivedAt.Add(-time.Minute)
		if diskEvidence(v, now, "docker") != unknown {
			t.Fatal("gap treated as sustained pressure")
		}
		if diskEvidence(samples(), now.Add(4*time.Minute), "docker") != unknown {
			t.Fatal("stale pressure is current")
		}
	})
	t.Run("absence requires complete inventory", func(t *testing.T) {
		v := samples()
		if serviceEvidence(v, now, id, false) != bad {
			t.Fatal("complete missing inventory did not alert")
		}
		v[1].Report.Partial = true
		if serviceEvidence(v, now, id, false) != unknown {
			t.Fatal("partial inventory established absence")
		}
		v[2].Report.Services = []api.ServiceMetrics{{ResourceId: id, State: "running"}}
		if serviceEvidence(v, now, id, false) != good {
			t.Fatal("explicit running state did not recover runtime")
		}
	})
	t.Run("health cannot recover from absence or starting", func(t *testing.T) {
		v := samples()
		unhealthy := api.MetricUnhealthy
		for i := range v {
			v[i].Report.Services = []api.ServiceMetrics{{ResourceId: id, State: "running", Health: &unhealthy}}
		}
		if serviceEvidence(v, now, id, true) != bad {
			t.Fatal("sustained unhealthy did not alert")
		}
		v[2].Report.Services[0].Health = nil
		if serviceEvidence(v, now, id, true) != unknown {
			t.Fatal("missing health recovered")
		}
		starting := api.MetricHealthStarting
		v[2].Report.Services[0].Health = &starting
		if serviceEvidence(v, now, id, true) != unknown {
			t.Fatal("starting health recovered")
		}
		healthy := api.MetricHealthy
		v[2].Report.Services[0].Health = &healthy
		if serviceEvidence(v, now, id, true) != good {
			t.Fatal("affirmative healthy did not recover")
		}
	})
}
