package metrics

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"math"
)

const MaxServices = 40

func Valid(r api.MetricReport) bool {
	if len(r.Services) > MaxServices || (r.Host.DiskSource != "docker" && r.Host.DiskSource != "agent") || !percentage(r.Host.CpuPercent, 100) || !usage(r.Host.Memory) || !usage(r.Host.Disk) {
		return false
	}
	seen := map[uuid.UUID]bool{}
	for _, s := range r.Services {
		if s.ResourceId == uuid.Nil || s.UnitId == uuid.Nil || seen[s.ResourceId] || !percentage(s.CpuPercent, 100000) || (s.MemoryUsedBytes != nil && *s.MemoryUsedBytes < 0) {
			return false
		}
		switch s.State {
		case "running", "stopped", "paused", "restarting", "unknown":
		default:
			return false
		}
		if s.Health != nil && !s.Health.Valid() {
			return false
		}
		seen[s.ResourceId] = true
	}
	return true
}
func percentage(v *float64, max float64) bool {
	return v == nil || !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= max
}
func usage(v *api.MetricUsage) bool {
	return v == nil || v.TotalBytes > 0 && v.UsedBytes >= 0 && v.UsedBytes <= v.TotalBytes
}
