package execution

import (
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// windowTestEntry is the ledger entry a base records for an operation row.
func windowTestEntry(t *testing.T, row portability.Row, p deployment.Plan, recorded time.Time) portability.AppliedEntry {
	t.Helper()
	identity, err := portability.CommandIdentity(row["command"], portability.CommandIdentityVersion)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := recoveryValue[uuid.UUID](row, "id")
	generation, _ := recoveryValue[int64](row, "generation")
	return portability.AppliedEntry{Environment: p.EnvironmentID, Operation: id, Generation: generation, Identity: identity, Outcome: "applied", RuntimePlan: &p.ID, Revision: &p.RevisionID, PlanHash: p.Hash, State: "succeeded", Sequence: 1, RecordedAt: recorded, Exact: true}
}

// windowTestHistory turns a full archive into a windowed base: it keeps only the environment's
// head as a row, and records every change from floor up in its ledger.
func windowTestHistory(t *testing.T, a portability.Archive, plans []deployment.Plan, floor int64) portability.Archive {
	t.Helper()
	history := &portability.RecoveryHistory{WindowStart: a.CreatedAt.Add(-portability.RecoveryWindowTarget), TargetDays: 90, Environments: []portability.RecoveryEnvironment{{Environment: plans[0].EnvironmentID, Floor: floor, Whole: floor == 0}}, Applied: []portability.AppliedEntry{}, Settled: []portability.SettledOperation{}, Superseded: []portability.SupersededBase{}}
	rows := a.Tables["execution_operations"]
	for i, row := range rows {
		generation, _ := recoveryValue[int64](row, "generation")
		if generation >= floor {
			history.Applied = append(history.Applied, windowTestEntry(t, row, plans[i], a.CreatedAt.Add(-time.Duration(len(rows)-i)*time.Hour)))
		}
	}
	a.Tables = maps.Clone(a.Tables)
	a.Tables["execution_operations"] = rows[len(rows)-1:]
	a.Recovery = history
	return a
}

func windowTestChain(t *testing.T, changes int) (portability.Archive, portability.Archive, []deployment.Plan) {
	t.Helper()
	backup, p := appliedTestArchive(t)
	backup.Tables["specification_revisions"] = nil
	plans := []deployment.Plan{p}
	newest := backup
	for generation := int64(2); generation <= int64(changes); generation++ {
		p.ID = uuid.New()
		p = p.WithHash()
		plans = append(plans, p)
		newest = appliedTestDeploy(newest, p, generation)
	}
	newest.CreatedAt = backup.CreatedAt.Add(time.Hour)
	return backup, newest, plans
}

// A windowed base keeps the history of applied runtimes as ledger entries, so it still shows that
// a backup's runtime is its ancestor when it no longer carries that runtime's operation row.
func TestWindowedBaseHoldsAnOlderBackupsRuntime(t *testing.T) {
	backup, full, plans := windowTestChain(t, 3)
	old, err := buildAppliedOrigin(backup, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	env := plans[0].EnvironmentID
	windowed, err := buildAppliedOrigin(windowTestHistory(t, full, plans, 1), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if order, err := compareApplied(windowed, windowed.Environments[env], old, old.Environments[env]); err != nil || order != 1 {
		t.Fatal("a backup inside the window was not recognised as the base's ancestor", order, err)
	}

	t.Run("below_the_floor", func(t *testing.T) {
		beyond, err := buildAppliedOrigin(windowTestHistory(t, full, plans, 2), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		_, err = compareApplied(beyond, beyond.Environments[env], old, old.Environments[env])
		var window recoveryWindowError
		if !errors.As(err, &window) || !strings.Contains(err.Error(), "use a management backup taken after") {
			t.Fatal("a backup older than the window was not refused with its dates", err)
		}
	})
	t.Run("missing_inside_the_window", func(t *testing.T) {
		gap := windowTestHistory(t, full, plans, 1)
		gap.Recovery.Applied = gap.Recovery.Applied[1:]
		diverged, err := buildAppliedOrigin(gap, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		_, err = compareApplied(diverged, diverged.Environments[env], old, old.Environments[env])
		var window recoveryWindowError
		if err == nil || errors.As(err, &window) {
			t.Fatal("a runtime inside the window that the base doesn't hold was not refused as divergent", err)
		}
	})
	t.Run("changed_identity", func(t *testing.T) {
		forked := windowTestHistory(t, full, plans, 1)
		forked.Recovery.Applied[0].Identity = "v2:" + strings.Repeat("0", 64)
		diverged, err := buildAppliedOrigin(forked, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = compareApplied(diverged, diverged.Environments[env], old, old.Environments[env]); err == nil {
			t.Fatal("a ledger entry for a different command was accepted as the backup's runtime")
		}
	})
}

// A backup's unfinished copy of an operation that ended after it takes the final state the newest
// base records, and only that: a copy that progressed further, or a different command, conflicts.
func TestRecoveryEndsGapWorkFromTheNewestBase(t *testing.T) {
	_, full, plans := windowTestChain(t, 2)
	history := windowTestHistory(t, full, plans, 1).Recovery
	row := appliedTestDeploy(full, plans[0], 1).Tables["execution_operations"][0]
	recoverySet(row, "state", "running")
	recoverySet(row, "report_sequence", 0)
	recoverySet(row, "runtime_outcome", nil)
	recoverySet(row, "runtime_plan_id", nil)
	// The ledger's entry for generation 1 describes the same command.
	history.Applied[0].Operation, _ = recoveryValue[uuid.UUID](row, "id")
	history.Applied[0].Identity, _ = portability.CommandIdentity(row["command"], portability.CommandIdentityVersion)
	ended, err := endRecoveryGapWork(row, history)
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := recoveryValue[string](ended, "state"); state != "succeeded" {
		t.Fatal("the backup's unfinished copy kept its stale state", state)
	}
	if outcome, _ := portability.ReadRuntimeOutcome(ended); outcome.Kind != "applied" || outcome.Plan != plans[0].ID {
		t.Fatal("the backup's copy lost the runtime the base recorded", outcome)
	}

	progressed := appliedTestRow(map[string]any{})
	for key, value := range row {
		progressed[key] = value
	}
	recoverySet(progressed, "report_sequence", 5)
	if _, err := endRecoveryGapWork(progressed, history); err == nil {
		t.Fatal("a copy that progressed beyond the base's final state was overwritten")
	}
	history.Applied[0].Identity = "v2:" + strings.Repeat("1", 64)
	if _, err := endRecoveryGapWork(row, history); err == nil {
		t.Fatal("a different command took the base's final state")
	}

	unknown := appliedTestDeploy(full, plans[1], 3).Tables["execution_operations"][2]
	recoverySet(unknown, "state", "running")
	same, err := endRecoveryGapWork(unknown, history)
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := recoveryValue[string](same, "state"); state != "running" {
		t.Fatal("an operation the base records nothing for was given a state")
	}

	settled := appliedTestDeploy(full, plans[1], 3).Tables["execution_operations"][2]
	recoverySet(settled, "state", "dispatched")
	recoverySet(settled, "runtime_outcome", nil)
	recoverySet(settled, "runtime_plan_id", nil)
	id, _ := recoveryValue[uuid.UUID](settled, "id")
	history.Settled = append(history.Settled, portability.SettledOperation{Operation: id, State: "failed", Sequence: 2, EndedAt: time.Now()})
	ended, err = endRecoveryGapWork(settled, history)
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := recoveryValue[string](ended, "state"); state != "failed" {
		t.Fatal("a root the base settled kept its stale state", state)
	}
}

// A backup that shows live an environment the newest base no longer carries is older than the
// window: the base carries every environment that is live or changed inside it.
func TestRecoveryRefusesABackupShowingARetiredPreviewLive(t *testing.T) {
	backup, full, plans := windowTestChain(t, 2)
	newest := windowTestHistory(t, full, plans, 1)
	preview := uuid.New()
	created := newest.CreatedAt.Add(-200 * 24 * time.Hour)
	backup.Tables["environments"] = append(backup.Tables["environments"], appliedTestRow(map[string]any{"id": preview, "name": "pr-7", "purpose": "preview", "created_at": created, "runtime_operation_id": nil}))
	backup.Tables["preview_requests"] = []portability.Row{appliedTestRow(map[string]any{"environment_id": preview, "state": "ready"})}
	for _, row := range backup.Tables["environments"] {
		if _, named := row["created_at"]; !named {
			recoverySet(row, "created_at", created)
			recoverySet(row, "purpose", "standard")
		}
	}
	source := recoverySource{Archive: newest, History: newest.Recovery}
	err := refuseRetiredBeyondWindow(backup, source)
	var window recoveryWindowError
	if !errors.As(err, &window) || !window.Retired || window.Environment != "pr-7" {
		t.Fatal("a backup showing a retired preview live was not refused", err)
	}
	backup.Tables["preview_requests"] = []portability.Row{appliedTestRow(map[string]any{"environment_id": preview, "state": "deleted"})}
	if err := refuseRetiredBeyondWindow(backup, source); err != nil {
		t.Fatal("a backup showing the preview retired was refused", err)
	}
	backup.Tables["preview_requests"] = []portability.Row{appliedTestRow(map[string]any{"environment_id": preview, "state": "ready"})}
	recoverySet(backup.Tables["environments"][1], "created_at", newest.CreatedAt.Add(time.Minute))
	if err := refuseRetiredBeyondWindow(backup, source); err != nil {
		t.Fatal("an environment created after the newest base was refused", err)
	}
}
