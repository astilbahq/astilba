package agent

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

const deadlineNote = "The admission deadline passed before execution."

// Every command kind the contract defines has a deadline rule, and the agent runs no other, so a
// new kind needs a decision about its deadline before it can run at all.
func TestEveryCommandKindHasADeadlineRule(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../api/api.gen.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[api.ExecutionCommandKind]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if kind, ok := spec.Type.(*ast.Ident); ok && kind.Name == "ExecutionCommandKind" {
			for _, v := range spec.Values {
				value, err := strconv.Unquote(v.(*ast.BasicLit).Value)
				if err != nil {
					t.Fatal(err)
				}
				kinds[api.ExecutionCommandKind(value)] = true
			}
		}
		return true
	})
	if len(kinds) == 0 {
		t.Fatal("found no command kinds in the contract")
	}
	for kind := range kinds {
		if _, ok := commandKinds[kind]; !ok {
			t.Errorf("command kind %s has no deadline rule", kind)
		}
	}
	for kind := range commandKinds {
		if !kinds[kind] {
			t.Errorf("deadline rule for command kind %s, which the contract doesn't define", kind)
		}
	}
}

// lateDeployment freezes a deployment phase first taken an hour after its deadline. When reserved
// is set, the host holds that deployment's reservation.
func lateDeployment(t *testing.T, phase string, reserved bool) maintenanceContinuationFixture {
	t.Helper()
	f := expiredMaintenance(t, false)
	f.body.Maintenance, f.body.Phase = nil, "reserve"
	f.freezeBody()
	if reserved {
		if _, err := f.journal.reserveCrossHost(f.command, f.body); err != nil {
			t.Fatal(err)
		}
	}
	f.body.Phase = phase
	f.freezeBody()
	f.command.Id = uuid.New()
	return f
}

func (f *maintenanceContinuationFixture) outcome(t *testing.T) api.AgentReport {
	t.Helper()
	if err := f.engine.Execute(context.Background(), f.command); err != nil {
		t.Fatal(err)
	}
	r, err := f.journal.Record(f.command.Id)
	if err != nil {
		t.Fatal(err)
	}
	return r.Report
}

// A late phase that would start something never runs, whatever the host holds. The fixture's
// Docker daemon fails the test on any change.
func TestLatePhasesThatStartWorkAreCancelledUnrun(t *testing.T) {
	for _, phase := range []string{"resolve", "observe", "reserve", "prepare", "hook", "probe", "publish"} {
		t.Run(phase, func(t *testing.T) {
			f := lateDeployment(t, phase, phase != "reserve" && phase != "resolve" && phase != "observe")
			if r := f.outcome(t); r.State != "cancelled" || r.Note != deadlineNote {
				t.Fatalf("late %s ended %s: %s", phase, r.State, r.Note)
			}
		})
	}
	for _, phase := range []string{"reserve", "pause", "effect", "restore"} {
		t.Run("maintenance-"+phase, func(t *testing.T) {
			f := expiredMaintenance(t, phase != "reserve")
			f.body.Phase = phase
			f.freezeBody()
			if r := f.outcome(t); r.State != "cancelled" || r.Note != deadlineNote {
				t.Fatalf("late %s ended %s: %s", phase, r.State, r.Note)
			}
		})
	}
}

// A late phase that ends a reservation runs only on the host that holds it. Abort also tells a
// host that holds nothing that there is nothing to clean up, which changes nothing there.
func TestLateEndingPhasesNeedTheHostToHoldTheirWork(t *testing.T) {
	for _, tc := range []struct {
		phase    string
		reserved bool
		runs     bool
	}{
		{"retire", true, true},
		{"complete", true, true},
		{"abort", true, true},
		{"retire", false, false},
		{"complete", false, false},
		{"abort", false, true},
	} {
		t.Run(tc.phase+map[bool]string{true: "-held", false: "-unheld"}[tc.reserved], func(t *testing.T) {
			f := lateDeployment(t, tc.phase, tc.reserved)
			r := f.outcome(t)
			if ran := r.Note != deadlineNote; ran != tc.runs {
				t.Fatalf("late %s ended %s: %s", tc.phase, r.State, r.Note)
			}
		})
	}
	f := expiredMaintenance(t, false)
	f.body.Phase = "resume-target"
	f.freezeBody()
	if r := f.outcome(t); r.Note != deadlineNote {
		t.Fatalf("late resume-target on a host holding nothing ended %s: %s", r.State, r.Note)
	}
}

// A late ending phase for another parent's reservation is refused before it is taken.
func TestLateEndingPhaseForAnotherReservationIsRefused(t *testing.T) {
	f := lateDeployment(t, "abort", true)
	other := lateDeployment(t, "abort", false)
	other.engine, other.journal = f.engine, f.journal
	other.command.ServerId = f.command.ServerId
	if err := other.engine.Execute(context.Background(), other.command); err == nil {
		t.Fatal("a host ran an ending phase for a reservation it doesn't hold")
	}
	if held, err := f.journal.crossHost(); err != nil || held.ParentID != f.body.ParentID {
		t.Fatal("another parent's late abort changed the reservation", err)
	}
}

// A phase without a deadline rule doesn't run, early or late.
func TestUnclassifiedPhaseIsRefused(t *testing.T) {
	f := lateDeployment(t, "unclassified", true)
	f.command.Deadline = f.command.Deadline.Add(2 * 3600e9)
	if r := f.outcome(t); r.State != "failed" || r.Note != "Unsupported coordinated phase." {
		t.Fatalf("an unclassified phase ended %s: %s", r.State, r.Note)
	}
}

// journalBytes is every key and value the journal holds, by bucket path.
func journalBytes(t *testing.T, j *Journal) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(prefix string, b *bolt.Bucket)
	walk = func(prefix string, b *bolt.Bucket) {
		_ = b.ForEach(func(k, v []byte) error {
			if v == nil {
				walk(prefix+string(k)+"/", b.Bucket(k))
				return nil
			}
			out[prefix+string(k)] = string(v)
			return nil
		})
	}
	if err := j.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			walk(string(name)+"/", b)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// A late ending phase on a host that holds no reservation only says so. Apart from the command's
// own record and the generation it raises, the journal is byte-identical, no reservation appears,
// and the fixture's Docker daemon fails the test on any change to the runtime.
func TestLateEndingPhaseOnAnUnreservedHostChangesNothing(t *testing.T) {
	for _, tc := range []struct{ phase, note string }{
		{"abort", "No candidate reservation was installed on this host."},
		{"resume-source", "This host never installed a maintenance reservation; its source was retained."},
		{"complete", "This host never installed a maintenance reservation; its source was retained."},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			f := expiredMaintenance(t, false)
			if tc.phase == "abort" {
				f = lateDeployment(t, "abort", false)
			} else {
				f.body.Phase = tc.phase
				f.freezeBody()
			}
			before := journalBytes(t, f.journal)
			r := f.outcome(t)
			if r.State != "succeeded" || r.Note != tc.note {
				t.Fatalf("late %s on an unreserved host ended %s: %s", tc.phase, r.State, r.Note)
			}
			after := journalBytes(t, f.journal)
			delete(after, "operations/"+f.command.Id.String())
			for key := range after {
				if strings.HasPrefix(key, "generations/") && before[key] != after[key] {
					delete(after, key)
					delete(before, key)
				}
			}
			if !maps.Equal(before, after) {
				t.Fatal("acknowledging held work the host doesn't have changed its journal")
			}
			if held, err := f.journal.hasCrossHostBarrier(); err != nil || held {
				t.Fatal("acknowledging nothing installed a reservation", err)
			}
		})
	}
}
