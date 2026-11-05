package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
)

// Prices from AWS's Price List Bulk API (RESULTS.md gives dates and sources), USD,
// on demand, no free tier, Savings Plans or reserved capacity.
const hoursPerMonth = 730

type region struct {
	name                                      string
	gp3Single, gp3Multi, backupGB             float64
	fargateCPU, fargateGB                     float64
	albHour, lcuHour, outGB, crossAZGB        float64
	logsGB, logsStoredGB, ipv4Hour, creditCPU float64
}

var regions = []region{
	{name: "us-east-1", gp3Single: 0.115, gp3Multi: 0.23, backupGB: 0.095, fargateCPU: 0.03238, fargateGB: 0.00356, albHour: 0.0225, lcuHour: 0.008, outGB: 0.09, crossAZGB: 0.01, logsGB: 0.50, logsStoredGB: 0.03, ipv4Hour: 0.005, creditCPU: 0.075},
	{name: "eu-west-1", gp3Single: 0.127, gp3Multi: 0.254, backupGB: 0.095, fargateCPU: 0.03238, fargateGB: 0.00356, albHour: 0.0252, lcuHour: 0.008, outGB: 0.09, crossAZGB: 0.01, logsGB: 0.57, logsStoredGB: 0.03, ipv4Hour: 0.005, creditCPU: 0.075},
}

// speed is how many vCPU-seconds of the class one measured core-second becomes:
// Geekbench 6 single-core, Apple M5 Pro about 4242, c7g.large 1467, r8g 1882. AWS's
// claim of up to 25% over Graviton2 gives about 1200 for Graviton2. Composite scores
// are not a PostgreSQL benchmark, so run with --speed-scale to see the sensitivity.
type class struct {
	name                                 string
	vcpu                                 int
	memGiB                               float64
	baseline                             float64 // sustained vCPUs covered by earned credits; 0 for fixed performance
	maxCredits                           float64
	speed                                float64
	singleUS, multiUS, singleEU, multiEU float64
	listsPostgres18                      bool // on AWS's class support page, checked 2026-11-05
}

var classes = []class{
	{"db.t4g.micro", 2, 1, 0.2, 288, 3.5, 0.016, 0.032, 0.017, 0.035, false},
	{"db.t4g.small", 2, 2, 0.4, 576, 3.5, 0.032, 0.065, 0.035, 0.069, false},
	{"db.t4g.medium", 2, 4, 0.4, 576, 3.5, 0.065, 0.129, 0.069, 0.138, false},
	{"db.t4g.large", 2, 8, 0.6, 864, 3.5, 0.129, 0.258, 0.138, 0.277, false},
	{"db.t4g.xlarge", 4, 16, 1.6, 2304, 3.5, 0.258, 0.517, 0.277, 0.554, false},
	{"db.m8g.large", 2, 8, 0, 0, 2.25, 0.168, 0.336, 0.186, 0.372, false},
	{"db.m8g.xlarge", 4, 16, 0, 0, 2.25, 0.336, 0.672, 0.372, 0.744, false},
	{"db.m8gd.large", 2, 8, 0, 0, 2.25, 0.1976, 0.3952, 0.228, 0.456, true},
	{"db.m8gd.xlarge", 4, 16, 0, 0, 2.25, 0.3952, 0.7904, 0.456, 0.912, true},
	// AWS's table says "All available versions". Its vCPUs are Ice Lake hyperthreads, so they
	// take the conservative Graviton2 ratio.
	{"db.m6i.large", 2, 8, 0, 0, 3.5, 0.178, 0.356, 0.197, 0.394, true},
	{"db.m6i.xlarge", 4, 16, 0, 0, 3.5, 0.356, 0.712, 0.394, 0.788, true},
	// Graviton5 at Graviton4's ratio for want of a benchmark. The eu-west-1 list has no price.
	{"db.m9g.large", 2, 8, 0, 0, 2.25, 0.183, 0.366, 0, 0, true},
}

// Valid Fargate ARM task sizes, smallest memory for each CPU step we might need.
var fargateCPUs = []struct {
	vcpu, minGB, maxGB, step float64
}{{0.25, 0.5, 2, 0.5}, {0.5, 1, 4, 1}, {1, 2, 8, 1}, {2, 4, 16, 1}, {4, 8, 30, 1}, {8, 16, 60, 4}, {16, 32, 120, 8}}

const fargateSpeed = 3.5 // Fargate ARM tasks are treated as Graviton2, the conservative assumption

// targetUtilisation leaves room for deployment bursts, sweeps and autovacuum spikes.
const targetUtilisation = 0.6

type measured struct {
	name                           string
	agents, dbCores, appCores      float64
	appMemGiB                      float64
	agentInBps, agentOutBps        float64 // per agent, at the application's edge
	dbBps, logBps, walBps, dbBytes float64 // whole fleet
}

func load(path string) (measured, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return measured{}, err
	}
	var r result
	if err = json.Unmarshal(raw, &r); err != nil {
		return measured{}, err
	}
	var before, after snapshot
	_ = json.Unmarshal(r.Before, &before)
	_ = json.Unmarshal(r.After, &after)
	m := measured{name: r.Name}
	m.agents, _ = r.Options["agents"].(float64)
	if m.agents == 0 {
		return m, errors.New(path + ": no agents")
	}
	sec := r.Seconds
	m.dbCores = float64(r.DBAfter.CPUMicros-r.DBBefore.CPUMicros) / 1e6 / sec
	m.appCores = float64(r.AppAfter.CPUMicros-r.AppBefore.CPUMicros) / 1e6 / sec
	m.appMemGiB = float64(r.AppAfter.MemoryAnon) / (1 << 30)
	m.agentInBps = float64(r.GenAfter.NetTX-r.GenBefore.NetTX) / sec / m.agents
	m.agentOutBps = float64(r.GenAfter.NetRX-r.GenBefore.NetRX) / sec / m.agents
	m.dbBps = float64(r.DBAfter.NetRX-r.DBBefore.NetRX+r.DBAfter.NetTX-r.DBBefore.NetTX) / sec
	m.logBps = float64(r.AppLogBytes) / sec
	m.walBps = (after.WALLSN - before.WALLSN) / sec
	m.dbBytes = after.DatabaseBytes
	return m, nil
}

type bill struct {
	database, storage, backup, compute, alb, transfer, ifCrossAZ, logs, ipv4 float64
	task, dbClass, note                                                      string
}

func (b bill) total() float64 {
	return b.database + b.storage + b.backup + b.compute + b.alb + b.transfer + b.logs + b.ipv4
}

func fargate(m measured, scale float64, r region) (string, float64) {
	need := m.appCores * fargateSpeed * scale / targetUtilisation
	mem := math.Max(m.appMemGiB*2, 0.5)
	for _, f := range fargateCPUs {
		if f.vcpu < need || mem > f.maxGB {
			continue
		}
		gb := f.minGB
		for gb < mem {
			gb += f.step
		}
		return fmt.Sprintf("%g vCPU / %g GB", f.vcpu, gb), (f.vcpu*r.fargateCPU + gb*r.fargateGB) * hoursPerMonth
	}
	return "over 16 vCPU", math.Inf(1)
}

// classMonthly returns the instance cost at this load, or an explanation if it cannot
// hold it. Burstable classes are in Unlimited mode: above baseline they cost more per
// vCPU-hour rather than slowing down, until all their vCPUs are busy.
func classMonthly(c class, m measured, scale float64, r region, multi bool) (cost, used float64, status string) {
	used = m.dbCores * c.speed * scale
	hourly := c.singleUS
	switch {
	case r.name == "us-east-1" && multi:
		hourly = c.multiUS
	case r.name != "us-east-1" && !multi:
		hourly = c.singleEU
	case r.name != "us-east-1" && multi:
		hourly = c.multiEU
	}
	cost = hourly * hoursPerMonth
	if hourly == 0 {
		return cost, used, "not offered"
	}
	if used > float64(c.vcpu)*targetUtilisation {
		return cost, used, "too small"
	}
	// Half a GiB for the OS and backends, and room to cache the database one and a half
	// times over. A sizing rule, not a measurement: agents touch little of the metric table.
	if c.memGiB < 0.5+1.5*m.dbBytes/(1<<30) {
		return cost, used, "too small"
	}
	if c.baseline == 0 {
		return cost, used, "holds"
	}
	if used <= c.baseline {
		return cost, used, "holds, earns credits"
	}
	excess := used - c.baseline
	surcharge := excess * r.creditCPU * hoursPerMonth
	if multi {
		surcharge *= 2
	}
	// A full balance lasts this long; afterwards surplus credits are billed.
	return cost + surcharge, used, fmt.Sprintf("balance empty in %.0f h, then +$%.0f/mo", c.maxCredits/(excess*60), surcharge)
}

func priced(m measured, scale float64, r region, multi, postgres18Only bool) bill {
	var b bill
	best := math.Inf(1)
	for _, c := range classes {
		if postgres18Only && !c.listsPostgres18 {
			continue
		}
		cost, used, status := classMonthly(c, m, scale, r, multi)
		if status != "too small" && status != "not offered" && cost < best {
			best = cost
			b.database, b.dbClass, b.note = cost, c.name, fmt.Sprintf("%.2f vCPU; %s", used, status)
		}
	}
	gb := m.dbBytes / 1e9
	provisioned := math.Max(20, gb*3)
	b.storage = provisioned * r.gp3Single
	if multi {
		b.storage = provisioned * r.gp3Multi
	}
	// Seven days of point-in-time recovery; backup storage up to the database size is free.
	walGB := m.walBps * 86400 * 7 / 1e9
	b.backup = math.Max(0, walGB+gb-provisioned) * r.backupGB
	b.task, b.compute = fargate(m, scale, r)
	gbPerMonth := func(bps float64) float64 { return bps * 3600 * hoursPerMonth / 1e9 }
	processedPerHour := (m.agentInBps + m.agentOutBps) * m.agents * 3600 / 1e9
	// One long poll plus one kept-alive connection per agent; ALB rates are per dimension.
	lcus := math.Max(2*m.agents/3000, processedPerHour)
	b.alb = (r.albHour + lcus*r.lcuHour) * hoursPerMonth
	b.transfer = gbPerMonth(m.agentOutBps*m.agents) * r.outGB
	// Each crossing is billed in both directions. Placing the task in the database's zone
	// avoids it, so it is reported beside the total rather than in it.
	b.ifCrossAZ = gbPerMonth(m.dbBps) * 2 * r.crossAZGB
	b.logs = gbPerMonth(m.logBps) * (r.logsGB + r.logsStoredGB)
	b.ipv4 = 3 * r.ipv4Hour * hoursPerMonth // two load balancer nodes and one task
	return b
}

func runCost(args []string) error {
	f := flag.NewFlagSet("cost", flag.ContinueOnError)
	scale := f.Float64("speed-scale", 1, "multiply every core conversion, to test sensitivity")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() == 0 {
		return errors.New("usage: fleet-cost cost [--speed-scale n] <result.json>...")
	}
	runs := []measured{}
	for _, path := range f.Args() {
		m, err := load(path)
		if err != nil {
			return err
		}
		runs = append(runs, m)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].agents < runs[j].agents })
	fmt.Printf("Burstable occupancy at speed scale %g, us-east-1 single-AZ:\n\n| Run | Class | vCPUs used | Baseline | State | $/month |\n| --- | --- | ---: | ---: | --- | ---: |\n", *scale)
	for _, m := range runs {
		for _, c := range classes {
			cost, used, status := classMonthly(c, m, *scale, regions[0], false)
			fmt.Printf("| %s | %s | %.2f | %.1f | %s | %.0f |\n", m.name, c.name, used, c.baseline, status, cost)
		}
	}
	for _, r := range regions {
		for _, variant := range []struct {
			name              string
			multi, postgres18 bool
		}{{"single-AZ, cheapest class that holds", false, false}, {"Multi-AZ, cheapest class that holds", true, false}, {"single-AZ, classes AWS lists for PostgreSQL 18", false, true}, {"Multi-AZ, classes AWS lists for PostgreSQL 18", true, true}} {
			fmt.Printf("\n%s, %s:\n\n| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |\n| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n", r.name, variant.name)
			for _, m := range runs {
				b := priced(m, *scale, r, variant.multi, variant.postgres18)
				fmt.Printf("| %s | %s (%s) | %.0f | %.1f | %s | %.0f | %.0f | %.1f | %.1f | %.0f | %.3f | %.3f |\n", m.name, b.dbClass, b.note, b.database, b.storage+b.backup, b.task, b.compute, b.alb+b.ipv4, b.transfer, b.logs, b.total(), b.total()/m.agents, b.ifCrossAZ/m.agents)
			}
		}
	}
	return nil
}
