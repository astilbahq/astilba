package main

import (
	"math"
	"strings"
	"testing"
)

func classNamed(t *testing.T, name string) class {
	t.Helper()
	for _, c := range classes {
		if c.name == name {
			return c
		}
	}
	t.Fatal("unknown class " + name)
	return class{}
}

// Pricing conclusions rest on these: a burstable class costs its baseline for free,
// then bills surplus vCPUs in Unlimited mode, and it never counts as holding a load
// beyond its vCPUs or memory.
func TestBurstableClassBillsSurplusCredits(t *testing.T) {
	medium := classNamed(t, "db.t4g.medium")
	load := measured{dbCores: 0.2, dbBytes: 1 << 30} // 0.7 vCPU at the Graviton2 ratio
	cost, used, status := classMonthly(medium, load, 1, regions[0], false)
	if math.Abs(used-0.7) > 1e-9 {
		t.Fatalf("used %v", used)
	}
	// 0.3 surplus vCPU × $0.075 × 730 h on top of $0.065 × 730 h.
	if want := 0.065*730 + 0.3*0.075*730; math.Abs(cost-want) > 1e-9 {
		t.Fatalf("cost %v want %v", cost, want)
	}
	// A full balance of 576 credits drains at 0.3 × 60 credits an hour.
	if !strings.Contains(status, "balance empty in 32 h") {
		t.Fatalf("status %q", status)
	}
	if _, _, status = classMonthly(medium, measured{dbCores: 0.1, dbBytes: 1 << 30}, 1, regions[0], false); !strings.Contains(status, "earns credits") {
		t.Fatalf("below baseline: %q", status)
	}
	if _, _, status = classMonthly(medium, measured{dbCores: 0.4, dbBytes: 1 << 30}, 1, regions[0], false); status != "too small" {
		t.Fatalf("above utilisation target: %q", status)
	}
	if _, _, status = classMonthly(classNamed(t, "db.t4g.micro"), measured{dbCores: 0.01, dbBytes: 1 << 30}, 1, regions[0], false); status != "too small" {
		t.Fatalf("over memory: %q", status)
	}
}

func TestPricedPicksCheapestClassThatHolds(t *testing.T) {
	load := measured{agents: 1000, dbCores: 0.13, appCores: 0.15, appMemGiB: 0.13, dbBytes: 640e6, agentInBps: 115, agentOutBps: 120}
	if b := priced(load, 1, regions[0], false, false); b.dbClass != "db.t4g.small" {
		t.Fatalf("class %s", b.dbClass)
	}
	// Only classes AWS lists for PostgreSQL 18 are candidates here, and only where priced.
	if b := priced(load, 1, regions[0], false, true); b.dbClass != "db.m9g.large" {
		t.Fatalf("class %s", b.dbClass)
	}
	if b := priced(load, 1, regions[1], false, true); b.dbClass != "db.m8gd.large" {
		t.Fatalf("class %s", b.dbClass)
	}
}
