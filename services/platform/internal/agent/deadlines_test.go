package agent

import (
	"context"
	"encoding/json/jsontext"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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

// The agent runs a coordinated phase only where it is handled: recovery opening and closing in
// their executors before admission, every other phase by its deadline rule. Management's issued
// phases are checked against RunsCoordinatedPhase when each integration test ends.
func TestRunsCoordinatedPhaseOnlyWhereThePhaseIsHandled(t *testing.T) {
	deployed, maintained := lateDeployment(t, "reserve", false).body, expiredMaintenance(t, false).body
	for _, body := range []deployment.CrossHostCommand{deployed, maintained} {
		for _, phase := range []string{"drain", "maintenance-reserve", "links", ""} {
			body.Phase = phase
			if RunsCoordinatedPhase(body) {
				t.Errorf("phase %q runs without a rule", phase)
			}
		}
	}
	deployed.Phase, maintained.Phase = "pause", "probe"
	if RunsCoordinatedPhase(deployed) || RunsCoordinatedPhase(maintained) {
		t.Error("a phase runs for the other kind of operation")
	}
	for phase, executor := range map[string]func(api.ExecutionCommand) bool{recoveryOpenPhase: recoveryOpen, recoveryClosePhase: recoveryClose} {
		_, deploymentRule := deploymentPhases[phase]
		_, maintenanceRule := maintenancePhases[phase]
		if deploymentRule || maintenanceRule {
			t.Errorf("%s has a deadline rule it never reaches", phase)
		}
		f := lateDeployment(t, phase, false)
		if !RunsCoordinatedPhase(f.body) || !executor(f.command) {
			t.Errorf("%s doesn't reach its executor", phase)
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

// A late checkpoint runs only to extend a recovery root this host already holds. The base
// checkpoint, or one naming another root, starts something, so it keeps its deadline.
func TestLateCheckpointNeedsTheHeldRoot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sequence int64
		root     string
		runs     bool
	}{
		{"base", 0, "", false},
		{"later-in-held-root", 1, "", true},
		{"later-in-another-root", 1, strings.Repeat("e", 64), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := lateDeployment(t, "checkpoint", false)
			d := f.descriptor
			if tc.sequence > 0 {
				d.Sequence, d.PreviousHash, d.CheckpointID = tc.sequence, d.Digest, uuid.New()
			}
			if tc.root != "" {
				d.RootHash = tc.root
			}
			raw, _ := stablejson.Marshal(d)
			metadata := jsontext.Value(raw)
			f.command.RecoveryCheckpoint = &metadata
			r := f.outcome(t)
			if ran := r.Note != deadlineNote; ran != tc.runs {
				t.Fatalf("late %s checkpoint ended %s: %s", tc.name, r.State, r.Note)
			}
		})
	}
}

// A late retirement ends a unit this host's journal holds for its environment, and nothing else: an
// applied plan, or the record of a deployment it ran, even a first one that failed, which leaves no
// applied plan. On a host whose journal holds neither it is cancelled unrun, as is any other late
// visual action and a retirement whose body no longer matches its plan hash.
func TestLateRetirementNeedsTheHostToHoldItsUnit(t *testing.T) {
	late := func(t *testing.T, j *Journal, c api.ExecutionCommand) bool {
		t.Helper()
		runs, err := (&Engine{Journal: j}).runsLate(c)
		if err != nil {
			t.Fatal(err)
		}
		return runs
	}
	ran := func(t *testing.T, j *Journal, c api.ExecutionCommand) {
		t.Helper()
		if err := j.db.Update(func(tx *bolt.Tx) error {
			return j.put(tx, "operations", c.Id.String(), Record{Command: c, Report: api.AgentReport{Id: c.Id, State: "needs_attention", Sequence: 2}, Acknowledged: true})
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("nothing held", func(t *testing.T) {
		j, plan := testVisualJournal(t)
		if late(t, j, visualCommand(plan, "retire", 2)) {
			t.Fatal("a late retirement ran on a host that never ran the unit")
		}
	})
	t.Run("failed first deployment", func(t *testing.T) {
		j, plan := testVisualJournal(t)
		ran(t, j, visualCommand(plan, "deploy", 1))
		if !late(t, j, visualCommand(plan, "retire", 2)) {
			t.Fatal("a late retirement was cancelled where a failed deployment left the unit")
		}
	})
	t.Run("applied plan", func(t *testing.T) {
		j, plan := testVisualJournal(t)
		if err := j.saveUnit(unitState{Plan: plan, Expected: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
		retire := visualCommand(plan, "retire", 2)
		if !late(t, j, retire) {
			t.Fatal("a late retirement of a held unit was cancelled")
		}
		tampered := retire
		tampered.PlanHash = strings.Repeat("0", 64)
		if late(t, j, tampered) {
			t.Fatal("a late retirement ran without matching its plan hash")
		}
		for action := range visualActions {
			if action != "retire" && late(t, j, visualCommand(plan, action, 2)) {
				t.Fatalf("a late %s ran", action)
			}
		}
	})
	t.Run("another environment's unit", func(t *testing.T) {
		j, plan := testVisualJournal(t)
		other := plan
		other.EnvironmentID = uuid.New()
		other = other.WithHash()
		ran(t, j, visualCommand(other, "deploy", 1))
		if err := j.saveUnit(unitState{Plan: other, Expected: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
		if late(t, j, visualCommand(plan, "retire", 2)) {
			t.Fatal("a late retirement ran for another environment's unit")
		}
	})
}

// PLAT-369. Every command records when this host admitted it, and its work can be bounded by a
// lifetime from then, never ending before its deadline. A record an older agent accepted has no
// admission time: it reads back without one and keeps its deadline.
func TestWorkIsBoundedFromAdmission(t *testing.T) {
	j, p := testVisualJournal(t)
	command := func() api.ExecutionCommand {
		return api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: uuid.New(), Kind: "test", Action: "inspect", Generation: 1, PlanHash: strings.Repeat("a", 64), Deadline: time.Now().Add(time.Minute)}
	}
	c := command()
	before := time.Now()
	r, err := j.Accept(c)
	if err != nil || r.AcceptedAt == nil || r.AcceptedAt.Before(before) || r.AcceptedAt.After(time.Now()) {
		t.Fatalf("a command's admission was not recorded: %v (%v)", r.AcceptedAt, err)
	}
	if stored, err := j.Record(c.Id); err != nil || stored.AcceptedAt == nil || !stored.AcceptedAt.Equal(*r.AcceptedAt) {
		t.Fatal("the admission time did not survive the journal", err)
	}
	if end := r.workEnd(time.Hour); !end.Equal(r.AcceptedAt.Add(time.Hour)) {
		t.Fatal("a command's work is not bounded from its admission:", end)
	}
	if end := r.workEnd(time.Second); !end.Equal(c.Deadline) {
		t.Fatal("a command's work ended before its deadline:", end)
	}
	// An older agent's record, which has no field for its admission, as AcceptedAt is omitted
	// when empty.
	legacy := command()
	old := Record{Command: legacy, Report: api.AgentReport{Id: legacy.Id, Generation: legacy.Generation, PlanHash: legacy.PlanHash, Sequence: 1, State: "running", Steps: []api.ExecutionStep{}}, Checkpoint: "accepted"}
	if err = j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "operations", legacy.Id.String(), old) }); err != nil {
		t.Fatal(err)
	}
	stored, err := j.Accept(legacy)
	if err != nil || stored.AcceptedAt != nil || deployment.Hash(stored) != deployment.Hash(old) {
		t.Fatal("redelivering an older agent's record changed it", err)
	}
	if end := stored.workEnd(time.Hour); !end.Equal(legacy.Deadline) {
		t.Fatal("an older agent's record was not bounded by its deadline:", end)
	}
}
