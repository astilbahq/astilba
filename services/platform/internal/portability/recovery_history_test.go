package portability

import (
	"encoding/json/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Only a recovery base carries a recovery section, from the version that added it, and the section
// must describe the environments the base carries.
func TestRecoverySectionIsCheckedAgainstItsBase(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	base.Version, base.CreatedAt = RecoveryArchiveVersion, time.Now().UTC()
	var env uuid.UUID
	if err := json.Unmarshal(base.Tables["environments"][0]["id"], &env); err != nil {
		t.Fatal(err)
	}
	plan := uuid.New()
	entry := func(generation int64) AppliedEntry {
		return AppliedEntry{Environment: env, Operation: uuid.New(), Generation: generation, Identity: "v2:" + strings.Repeat("a", 64), Outcome: "applied", RuntimePlan: &plan, State: "succeeded", Sequence: 1, RecordedAt: base.CreatedAt.Add(-time.Hour), Exact: true}
	}
	valid := func() *RecoveryHistory {
		environments := []RecoveryEnvironment{}
		for _, row := range base.Tables["environments"] {
			var id uuid.UUID
			_ = json.Unmarshal(row["id"], &id)
			start := RecoveryEnvironment{Environment: id, Whole: true}
			if id == env {
				start = RecoveryEnvironment{Environment: id, Floor: 3}
			}
			environments = append(environments, start)
		}
		return &RecoveryHistory{WindowStart: base.CreatedAt.Add(-RecoveryWindowTarget), TargetDays: 90, Environments: environments, Applied: []AppliedEntry{entry(3), entry(4)}, Settled: []SettledOperation{}, Superseded: []SupersededBase{}}
	}
	decode := func(a Archive, recovery bool) error {
		encoded, err := stablejson.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeArchiveMode(encoded, recovery)
		return err
	}
	base.Recovery = valid()
	if err := decode(base, true); err != nil {
		t.Fatal("a valid recovery base was refused", err)
	}
	if decode(base, false) == nil {
		t.Fatal("an import accepted a recovery base")
	}
	for name, change := range map[string]func(a *Archive){
		"no section":               func(a *Archive) { a.Recovery = nil },
		"section on an older base": func(a *Archive) { a.Version = ArchiveVersion },
		"floor marked whole":       func(a *Archive) { a.Recovery.Environments[0].Whole = !a.Recovery.Environments[0].Whole },
		"entry below its floor":    func(a *Archive) { a.Recovery.Applied = append(a.Recovery.Applied, entry(2)) },
		"no entry at the floor":    func(a *Archive) { a.Recovery.Applied = a.Recovery.Applied[1:] },
		"environment not carried": func(a *Archive) {
			a.Recovery.Environments = append(a.Recovery.Environments, RecoveryEnvironment{Environment: uuid.New(), Whole: true})
		},
		"carried environment left out": func(a *Archive) { a.Recovery.Environments = a.Recovery.Environments[1:] },
		"two entries at a generation":  func(a *Archive) { a.Recovery.Applied = append(a.Recovery.Applied, entry(4)) },
		"removal naming a plan":        func(a *Archive) { a.Recovery.Applied[1].Outcome = "removed" },
		"unknown identity version":     func(a *Archive) { a.Recovery.Applied[1].Identity = "v1:" + strings.Repeat("a", 64) },
		"window after the capture":     func(a *Archive) { a.Recovery.WindowStart = a.CreatedAt.Add(time.Hour) },
		"settled and applied": func(a *Archive) {
			a.Recovery.Settled = []SettledOperation{{Operation: a.Recovery.Applied[0].Operation, State: "succeeded", EndedAt: a.CreatedAt}}
		},
		"settled while unfinished": func(a *Archive) {
			a.Recovery.Settled = []SettledOperation{{Operation: uuid.New(), State: "running", EndedAt: a.CreatedAt}}
		},
	} {
		altered := base
		altered.Recovery = valid()
		change(&altered)
		if decode(altered, true) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
