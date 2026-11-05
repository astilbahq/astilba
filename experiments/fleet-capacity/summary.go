package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type statement struct {
	Query         string  `json:"query"`
	Calls         float64 `json:"calls"`
	TotalExecTime float64 `json:"total_exec_time"`
	WALBytes      float64 `json:"wal_bytes"`
}

type snapshot struct {
	DatabaseBytes float64 `json:"databaseBytes"`
	WALLSN        float64 `json:"walLSN"`
	WALFsync      struct {
		Fsyncs float64 `json:"fsyncs"`
		Millis float64 `json:"millis"`
	} `json:"walFsync"`
	Tables []struct {
		Relname    string  `json:"relname"`
		TotalBytes float64 `json:"total_bytes"`
	} `json:"tables"`
}

type agentStats struct {
	Endpoints map[string]*endpointStats `json:"endpoints"`
	Deploys   struct {
		Started, Finished, Failed int64
		DeliveryMicros            map[string]int64
	} `json:"deploys"`
	Errors []string `json:"errors"`
}

// quantile reads an upper bound from log2 latency buckets: within a factor of two.
func quantile(after, before map[string]int64, q float64) float64 {
	keys := []int{}
	total := int64(0)
	for k, v := range after {
		n, _ := strconv.Atoi(k)
		if d := v - before[k]; d > 0 {
			keys = append(keys, n)
			total += d
		}
	}
	sort.Ints(keys)
	seen := int64(0)
	for _, k := range keys {
		s := strconv.Itoa(k)
		seen += after[s] - before[s]
		if float64(seen) >= q*float64(total) {
			return math.Pow(2, float64(k)) / 1000
		}
	}
	return 0
}

var statementLabels = []struct {
	pattern *regexp.Regexp
	label   string
}{
	{regexp.MustCompile(`SELECT workspace_id,role,\(credential_hash`), "AuthenticateAgent"},
	{regexp.MustCompile(`^SELECT t\.id,t\.unit_id,t\.application_id,t\.instance`), "ListTroubleshootingOffers"},
	{regexp.MustCompile(`SELECT command,cancel_requested,state FROM execution_operations`), "poll command"},
	{regexp.MustCompile(`UPDATE servers SET poll_id=\$2`), "poll lease take"},
	{regexp.MustCompile(`UPDATE servers SET poll_id=NULL`), "poll lease release"},
	{regexp.MustCompile(`^UPDATE servers SET last_seen=now\(\),capabilities`), "RecordAgentHeartbeat"},
	{regexp.MustCompile(`SELECT COALESCE\(NOT revoked AND NOT awaiting_transfer`), "lockAgentCredential"},
	{regexp.MustCompile(`INSERT INTO server_metrics`), "metrics insert"},
	{regexp.MustCompile(`DELETE FROM server_metrics WHERE server_id`), "metrics prune"},
	{regexp.MustCompile(`SELECT EXISTS\(SELECT 1 FROM managed_resources`), "metrics ownership"},
	{regexp.MustCompile(`(?i)^begin`), "BEGIN"},
	{regexp.MustCompile(`(?i)^commit`), "COMMIT"},
	{regexp.MustCompile(`(?i)^rollback`), "ROLLBACK"},
}

func label(query string) string {
	for _, l := range statementLabels {
		if l.pattern.MatchString(query) {
			return l.label
		}
	}
	if len(query) > 60 {
		return query[:60] + "…"
	}
	return query
}

func runSummary(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: fleet-capacity summary <result.json>...")
	}
	sort.Slice(args, func(i, j int) bool { return args[i] < args[j] })
	fmt.Println("| Run | Agents | q/s | q/s per agent | DB CPU cores | App CPU cores | DB anon+shm MiB | App anon MiB | Clients max / mean active | Agent B/s in / out per agent | DB B/s in / out per agent | WAL KiB/s | Logs B/s per agent | Heartbeat p50 / p99 ms | Non-2xx | pgbench -S TPS | Host load |")
	fmt.Println("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- | --- | ---: | ---: | --- | ---: | ---: | --- |")
	var details strings.Builder
	for _, path := range args {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var r result
		if err = json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		agents, _ := r.Options["agents"].(float64)
		perAgent := func(v float64) float64 {
			if agents == 0 {
				return math.NaN()
			}
			return v / agents
		}
		var statements []statement
		_ = json.Unmarshal(r.Statements, &statements)
		var queries, execMillis float64
		byLabel := map[string]*statement{}
		for _, s := range statements {
			queries += s.Calls
			execMillis += s.TotalExecTime
			l := label(s.Query)
			if byLabel[l] == nil {
				byLabel[l] = &statement{Query: l}
			}
			byLabel[l].Calls += s.Calls
			byLabel[l].TotalExecTime += s.TotalExecTime
			byLabel[l].WALBytes += s.WALBytes
		}
		var before, after agentStats
		_ = json.Unmarshal(r.StatsBefore, &before)
		_ = json.Unmarshal(r.StatsAfter, &after)
		rejected := int64(0)
		for name, e := range after.Endpoints {
			for status, n := range e.Status {
				var previous int64
				if b := before.Endpoints[name]; b != nil {
					previous = b.Status[status]
				}
				if !strings.HasPrefix(status, "2") {
					rejected += n - previous
				}
			}
		}
		beat := after.Endpoints["heartbeat"]
		p50, p99 := 0.0, 0.0
		if beat != nil {
			var previous map[string]int64
			if b := before.Endpoints["heartbeat"]; b != nil {
				previous = b.LatencyUS
			}
			p50, p99 = quantile(beat.LatencyUS, previous, 0.5), quantile(beat.LatencyUS, previous, 0.99)
		}
		maxClients, activeSum, activeN := 0.0, 0.0, 0.0
		for _, s := range r.Samples {
			var a struct{ Clients, Active float64 }
			if json.Unmarshal(s.Activity, &a) == nil {
				maxClients = math.Max(maxClients, a.Clients)
				activeSum += a.Active
				activeN++
			}
		}
		var b0, b1 snapshot
		_ = json.Unmarshal(r.Before, &b0)
		_ = json.Unmarshal(r.After, &b1)
		sec := r.Seconds
		tps, _ := r.Calibration["selectOnlyTPS"].(float64)
		fmt.Printf("| %s | %.0f | %.0f | %.2f | %.3f | %.3f | %.0f | %.0f | %.0f / %.2f | %.0f / %.0f | %.0f / %.0f | %.0f | %.0f | %.1f / %.1f | %d | %.0f | %s |\n",
			r.Name, agents, queries/sec, perAgent(queries/sec),
			float64(r.DBAfter.CPUMicros-r.DBBefore.CPUMicros)/1e6/sec, float64(r.AppAfter.CPUMicros-r.AppBefore.CPUMicros)/1e6/sec,
			float64(r.DBAfter.MemoryAnon+r.DBAfter.MemoryShm)/(1<<20), float64(r.AppAfter.MemoryAnon)/(1<<20),
			maxClients, activeSum/math.Max(activeN, 1),
			perAgent(float64(r.GenAfter.NetTX-r.GenBefore.NetTX)/sec), perAgent(float64(r.GenAfter.NetRX-r.GenBefore.NetRX)/sec),
			perAgent(float64(r.DBAfter.NetRX-r.DBBefore.NetRX)/sec), perAgent(float64(r.DBAfter.NetTX-r.DBBefore.NetTX)/sec),
			(b1.WALLSN-b0.WALLSN)/1024/sec, perAgent(float64(r.AppLogBytes)/sec), p50, p99, rejected, tps, r.LoadBefore)
		fmt.Fprintf(&details, "\n### %s\n\nWindow %.0fs; revision %s; ping %s; options %v.\n\n| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: |\n", r.Name, sec, r.Revision, r.Ping, r.Options)
		rows := make([]*statement, 0, len(byLabel))
		for _, s := range byLabel {
			rows = append(rows, s)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].TotalExecTime > rows[j].TotalExecTime })
		for i, s := range rows {
			if i == 15 {
				break
			}
			fmt.Fprintf(&details, "| %s | %.1f | %.3f | %.3f | %.1f | %.1f%% | %.0f |\n", strings.ReplaceAll(s.Query, "|", "\\|"), s.Calls/sec, perAgent(s.Calls/sec), s.TotalExecTime/math.Max(s.Calls, 1), s.TotalExecTime/sec, 100*s.TotalExecTime/math.Max(execMillis, 1), s.WALBytes/sec)
		}
		fmt.Fprintf(&details, "\nWAL fsync: %.0f/s, mean %.2f ms. Database: %.0f → %.0f bytes (%.0f bytes/s). Throttled µs: DB %d, app %d. Generator CPU %.3f cores. Load %s → %s.\n",
			(b1.WALFsync.Fsyncs-b0.WALFsync.Fsyncs)/sec, (b1.WALFsync.Millis-b0.WALFsync.Millis)/math.Max(b1.WALFsync.Fsyncs-b0.WALFsync.Fsyncs, 1),
			b0.DatabaseBytes, b1.DatabaseBytes, (b1.DatabaseBytes-b0.DatabaseBytes)/sec, r.DBAfter.ThrottledMicros-r.DBBefore.ThrottledMicros, r.AppAfter.ThrottledMicros-r.AppBefore.ThrottledMicros, float64(r.GenAfter.CPUMicros-r.GenBefore.CPUMicros)/1e6/sec, r.LoadBefore, r.LoadAfter)
		before0 := map[string]float64{}
		for _, t := range b0.Tables {
			before0[t.Relname] = t.TotalBytes
		}
		sort.Slice(b1.Tables, func(i, j int) bool {
			return b1.Tables[i].TotalBytes-before0[b1.Tables[i].Relname] > b1.Tables[j].TotalBytes-before0[b1.Tables[j].Relname]
		})
		fmt.Fprintf(&details, "\nLargest table growth: ")
		for i, t := range b1.Tables {
			if i == 6 {
				break
			}
			fmt.Fprintf(&details, "%s %+.0f (now %.0f); ", t.Relname, t.TotalBytes-before0[t.Relname], t.TotalBytes)
		}
		fmt.Fprintf(&details, "\n\nDeploys in window: started %d, finished %d, failed %d; delivery p50/p99 ms %.0f/%.0f. Errors kept since the generator started, including setup, capped at 200: %d.\n", after.Deploys.Started-before.Deploys.Started, after.Deploys.Finished-before.Deploys.Finished, after.Deploys.Failed-before.Deploys.Failed, quantile(after.Deploys.DeliveryMicros, before.Deploys.DeliveryMicros, 0.5), quantile(after.Deploys.DeliveryMicros, before.Deploys.DeliveryMicros, 0.99), len(after.Errors))
		if len(after.Errors) > 0 {
			fmt.Fprintf(&details, "Oldest kept error: %.200s\n", after.Errors[0])
		}
		if r.AppErrors != "" {
			fmt.Fprintf(&details, "\nApplication warnings:\n\n```\n%s\n```\n", r.AppErrors)
		}
	}
	fmt.Print(details.String())
	return nil
}
