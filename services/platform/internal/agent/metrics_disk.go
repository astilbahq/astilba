//go:build linux || darwin

package agent

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"golang.org/x/sys/unix"
	"math"
	"time"
)

const metricStatsTimeout = 250 * time.Millisecond

func metricDisk(path string) *api.MetricUsage {
	var s unix.Statfs_t
	if unix.Statfs(path, &s) != nil || s.Bsize <= 0 || s.Blocks > math.MaxInt64/uint64(s.Bsize) || s.Bavail > s.Blocks {
		return nil
	}
	total := int64(s.Blocks) * int64(s.Bsize)
	if total == 0 {
		return nil
	}
	return &api.MetricUsage{TotalBytes: total, UsedBytes: total - int64(s.Bavail)*int64(s.Bsize)}
}
