package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/metrics"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

type cpuCounters struct{ total, idle uint64 }
type containerCounters struct{ cpu, system uint64 }

// MetricCollector belongs to one sampling loop; counter baselines are not shared.
type MetricCollector struct {
	Journal    *Journal
	previous   *cpuCounters
	containers map[string]containerCounters
}

func hostCPU(raw string) (cpuCounters, bool) {
	line, _, _ := strings.Cut(raw, "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, false
	}
	var v cpuCounters
	for i := 1; i < len(fields) && i <= 8; i++ {
		n, e := strconv.ParseUint(fields[i], 10, 64)
		if e != nil || n > math.MaxUint64-v.total {
			return cpuCounters{}, false
		}
		v.total += n
		if i == 4 || i == 5 {
			v.idle += n
		}
	}
	return v, true
}
func hostMemory(raw string) *api.MetricUsage {
	var total, available int64
	found := false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[2] != "kB" {
			continue
		}
		if f[0] != "MemTotal:" && f[0] != "MemAvailable:" {
			continue
		}
		n, e := strconv.ParseInt(f[1], 10, 64)
		if e != nil || n < 0 || n > math.MaxInt64/1024 {
			return nil
		}
		if f[0] == "MemTotal:" {
			total = n * 1024
		} else {
			available = n * 1024
			found = true
		}
	}
	if total <= 0 || !found || available > total {
		return nil
	}
	return &api.MetricUsage{TotalBytes: total, UsedBytes: total - available}
}
func readMetricFile(path string) string {
	f, e := os.Open(path)
	if e != nil {
		return ""
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 65536))
	if e != nil {
		return ""
	}
	return string(raw)
}
func (c *MetricCollector) Collect(ctx context.Context, docker *client.Client, server uuid.UUID, agentDirectory string) api.MetricReport {
	out := api.MetricReport{Host: api.HostMetrics{DiskSource: "agent"}, Services: []api.ServiceMetrics{}}
	current, ok := hostCPU(readMetricFile("/proc/stat"))
	if ok {
		if c.previous != nil && current.total > c.previous.total && current.idle >= c.previous.idle {
			total, idle := current.total-c.previous.total, current.idle-c.previous.idle
			if idle <= total {
				v := 100 * float64(total-idle) / float64(total)
				out.Host.CpuPercent = &v
			}
		}
		c.previous = &current
	} else {
		c.previous = nil
		out.Partial = true
	}
	out.Host.Memory = hostMemory(readMetricFile("/proc/meminfo"))
	if out.Host.Memory == nil {
		out.Partial = true
	}
	diskPath := agentDirectory
	if docker != nil {
		info, e := docker.Info(ctx, client.InfoOptions{})
		if e == nil && info.Info.DockerRootDir != "" {
			diskPath = info.Info.DockerRootDir
			out.Host.DiskSource = "docker"
		} else {
			out.Partial = true
		}
	} else {
		out.Partial = true
	}
	out.Host.Disk = metricDisk(diskPath)
	if out.Host.Disk == nil {
		out.Partial = true
	}
	if docker == nil {
		return out
	}
	type identity struct{ resource, unit uuid.UUID }
	watched := map[string]identity{}
	desired, units := map[uuid.UUID]int{}, map[uuid.UUID]uuid.UUID{}
	order := []uuid.UUID{}
	groups := map[uuid.UUID][]api.ServiceMetrics{}
	if c.Journal != nil {
		plans, partial, err := c.Journal.metricUnits(server)
		out.Partial = out.Partial || partial
		if err != nil {
			out.Partial = true
			return out
		}
		for _, active := range plans {
			for _, app := range active.Plan.LocalApplications() {
				if _, exists := groups[app.ID]; exists || len(order) >= metrics.MaxServices {
					out.Partial = true
					continue
				}
				order = append(order, app.ID)
				groups[app.ID] = nil
				units[app.ID] = active.Plan.UnitID
				instances := active.Plan.InstanceIDs(app.ID)
				desired[app.ID] = len(instances)
				for _, instance := range instances {
					name := "/" + deployment.UnitName(instance)
					if _, exists := watched[name]; exists {
						out.Partial = true
						continue
					}
					watched[name] = identity{app.ID, active.Plan.UnitID}
				}
			}
		}
	}
	limit := metrics.MaxServices * (domain.MaxReplicas + 2)
	list, e := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Limit: limit + 1, Filters: client.Filters{}.Add("label", labelServer+"="+server.String()).Add("label", labelKind+"=visual-v1")})
	if e != nil {
		out.Partial = true
		return out
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].ID < list.Items[j].ID })
	if len(list.Items) > limit {
		out.Partial = true
	}
	next := map[string]containerCounters{}
	for _, item := range list.Items {
		resource, err := uuid.Parse(item.Labels[labelResource])
		if err != nil {
			out.Partial = true
			continue
		}
		unit, err := uuid.Parse(item.Labels["com.astilba.unit"])
		if err != nil {
			out.Partial = true
			continue
		}
		match := false
		for _, name := range item.Names {
			if c.Journal == nil {
				match = match || name == "/"+deployment.UnitName(resource)
			} else {
				expected, ok := watched[name]
				match = match || ok && expected.resource == resource && expected.unit == unit
			}
		}
		if !match {
			continue
		}
		if previous, exists := units[resource]; exists && previous != unit {
			out.Partial = true
			continue
		}
		if _, exists := groups[resource]; !exists {
			if len(order) >= metrics.MaxServices {
				out.Partial = true
				continue
			}
			order = append(order, resource)
			desired[resource] = 1
			units[resource] = unit
		}
		state := api.ServiceMetricsState(item.State)
		switch state {
		case "running", "paused", "restarting":
		case "created", "exited", "dead":
			state = "stopped"
		default:
			state = "unknown"
		}
		value := api.ServiceMetrics{ResourceId: resource, UnitId: unit, State: state}
		if item.Health != nil {
			health := api.ServiceMetricsHealth(item.Health.Status)
			if health.Valid() {
				value.Health = &health
			}
		}
		if state == "running" {
			if item.Health == nil {
				collectHealth(ctx, docker, item.ID, &value, &out.Partial)
			}
			c.collectContainer(ctx, docker, item.ID, &value, next, &out.Partial)
		}
		groups[resource] = append(groups[resource], value)
	}
	for _, id := range order {
		if len(groups[id]) == 0 {
			out.Partial = true
			out.Services = append(out.Services, api.ServiceMetrics{ResourceId: id, UnitId: units[id], State: "unknown"})
		} else {
			out.Services = append(out.Services, aggregateServiceMetrics(groups[id], desired[id], &out.Partial))
		}
	}
	c.containers = next
	return out
}

// Bound journal inventory independently of Docker's list so an entirely absent
// group remains unknown instead of disappearing from a complete metrics report.
func (j *Journal) metricUnits(server uuid.UUID) ([]unitState, bool, error) {
	units := []unitState{}
	partial := false
	err := j.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte("units")).Cursor()
		scanned := 0
		for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
			if scanned == metrics.MaxServices {
				partial = true
				break
			}
			scanned++
			var state unitState
			if err := j.get(tx, "units", string(key), &state); err != nil {
				return err
			}
			if state.Plan.ID == uuid.Nil {
				continue
			}
			if state.Plan.ServerID != server || state.Plan.UnitID.String() != string(key) {
				return errors.New("metrics unit identity mismatch")
			}
			units = append(units, state)
		}
		return nil
	})
	return units, partial, err
}

func aggregateServiceMetrics(samples []api.ServiceMetrics, expected int, partial *bool) api.ServiceMetrics {
	if len(samples) == 1 && expected == 1 {
		return samples[0]
	}
	v := api.ServiceMetrics{ResourceId: samples[0].ResourceId, UnitId: samples[0].UnitId, State: "stopped"}
	memory, cpu := int64(0), float64(0)
	memoryKnown, cpuKnown := len(samples) == expected, len(samples) == expected
	health := api.MetricNoHealthcheck
	healthKnown, unhealthy := true, len(samples) != expected
	if len(samples) != expected {
		*partial = true
	}
	for _, member := range samples {
		if member.State == "running" {
			v.State = "running"
			if member.MemoryUsedBytes == nil || memory > math.MaxInt64-*member.MemoryUsedBytes {
				memoryKnown = false
			} else {
				memory += *member.MemoryUsedBytes
			}
			if member.CpuPercent == nil {
				cpuKnown = false
			} else {
				cpu += *member.CpuPercent
			}
		} else {
			unhealthy = true
			if member.State != "stopped" {
				memoryKnown = false
				cpuKnown = false
				*partial = true
			}
		}
		if member.Health == nil {
			healthKnown = false
		} else {
			switch *member.Health {
			case "unhealthy":
				unhealthy = true
			case "starting":
				health = "starting"
			case "healthy":
				if health != "starting" {
					health = "healthy"
				}
			}
		}
	}
	if memoryKnown {
		v.MemoryUsedBytes = &memory
	} else {
		*partial = true
	}
	if cpuKnown {
		if cpu <= 100000 {
			v.CpuPercent = &cpu
		} else {
			*partial = true
		}
	}
	if unhealthy {
		health = "unhealthy"
		healthKnown = true
	}
	if healthKnown {
		v.Health = &health
	}
	return v
}
func (c *MetricCollector) collectContainer(ctx context.Context, docker *client.Client, id string, v *api.ServiceMetrics, next map[string]containerCounters, partial *bool) {
	statsCtx, cancel := context.WithTimeout(ctx, metricStatsTimeout)
	defer cancel()
	stats, e := docker.ContainerStats(statsCtx, id, client.ContainerStatsOptions{})
	if e != nil {
		*partial = true
		return
	}
	defer stats.Body.Close()
	var sample struct {
		CPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System uint64 `json:"system_cpu_usage"`
			CPUs   uint32 `json:"online_cpus"`
		} `json:"cpu_stats"`
		Memory struct {
			Usage *uint64 `json:"usage"`
		} `json:"memory_stats"`
	}
	if json.NewDecoder(io.LimitReader(stats.Body, 65536)).Decode(&sample) != nil || sample.Memory.Usage == nil || *sample.Memory.Usage > math.MaxInt64 {
		*partial = true
		return
	}
	memory := int64(*sample.Memory.Usage)
	v.MemoryUsedBytes = &memory
	now := containerCounters{sample.CPU.Usage.Total, sample.CPU.System}
	next[id] = now
	if previous, ok := c.containers[id]; ok && now.system > previous.system && now.cpu >= previous.cpu && sample.CPU.CPUs > 0 {
		percent := 100 * float64(now.cpu-previous.cpu) / float64(now.system-previous.system) * float64(sample.CPU.CPUs)
		if percent <= 100000 {
			v.CpuPercent = &percent
		}
	}
}

func collectHealth(ctx context.Context, docker *client.Client, id string, value *api.ServiceMetrics, partial *bool) {
	ctx, cancel := context.WithTimeout(ctx, metricStatsTimeout)
	defer cancel()
	inspected, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.State == nil {
		*partial = true
		return
	}
	health := api.MetricNoHealthcheck
	if inspected.Container.State.Health != nil {
		health = api.ServiceMetricsHealth(inspected.Container.State.Health.Status)
	}
	if !health.Valid() {
		*partial = true
		return
	}
	value.Health = &health
}
