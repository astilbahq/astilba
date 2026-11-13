package execution

import (
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
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
	known := func(uuid.UUID) bool { return true }
	ended, err := endRecoveryGapWork(row, history, known)
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := recoveryValue[string](ended, "state"); state != "succeeded" {
		t.Fatal("the backup's unfinished copy kept its stale state", state)
	}
	if outcome, _ := portability.ReadRuntimeOutcome(ended); outcome.Kind != "applied" || outcome.Plan != plans[0].ID {
		t.Fatal("the backup's copy lost the runtime the base recorded", outcome)
	}
	// A plan neither copy holds, such as an upgrade's target stored when it completed, can't be
	// named: the operation ends without the outcome, which recovery keeps as an entry instead.
	unnamed, err := endRecoveryGapWork(row, history, func(uuid.UUID) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := recoveryValue[string](unnamed, "state"); state != "succeeded" {
		t.Fatal("an operation whose plan isn't held kept its stale state", state)
	}
	if outcome, _ := portability.ReadRuntimeOutcome(unnamed); outcome.Kind != "" {
		t.Fatal("an operation named a plan neither copy holds", outcome)
	}

	progressed := appliedTestRow(map[string]any{})
	for key, value := range row {
		progressed[key] = value
	}
	recoverySet(progressed, "report_sequence", 5)
	if _, err := endRecoveryGapWork(progressed, history, known); err == nil {
		t.Fatal("a copy that progressed beyond the base's final state was overwritten")
	}
	history.Applied[0].Identity = "v2:" + strings.Repeat("1", 64)
	if _, err := endRecoveryGapWork(row, history, known); err == nil {
		t.Fatal("a different command took the base's final state")
	}

	unknown := appliedTestDeploy(full, plans[1], 3).Tables["execution_operations"][2]
	recoverySet(unknown, "state", "running")
	same, err := endRecoveryGapWork(unknown, history, known)
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
	ended, err = endRecoveryGapWork(settled, history, known)
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := recoveryValue[string](ended, "state"); state != "failed" {
		t.Fatal("a root the base settled kept its stale state", state)
	}
}

// A backup that shows live an environment the newest base no longer carries is older than the
// window: the base carries every environment whose runtime is applied or changed inside it.
func TestRecoveryRefusesABackupShowingARetiredPreviewLive(t *testing.T) {
	backup, full, plans := windowTestChain(t, 2)
	newest := windowTestHistory(t, full, plans, 1)
	preview, head := uuid.New(), uuid.New()
	created := newest.CreatedAt.Add(-200 * 24 * time.Hour)
	backup.Tables["environments"] = append(backup.Tables["environments"], appliedTestRow(map[string]any{"id": preview, "name": "pr-7", "purpose": "preview", "created_at": created, "runtime_operation_id": head}))
	operation := appliedTestRow(map[string]any{"id": head, "runtime_outcome": "applied", "runtime_plan_id": uuid.New()})
	backup.Tables["execution_operations"] = append(backup.Tables["execution_operations"], operation)
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
		t.Fatal("a backup showing a retired preview's runtime live was not refused", err)
	}
	recoverySet(operation, "runtime_outcome", "removed")
	recoverySet(operation, "runtime_plan_id", nil)
	if err := refuseRetiredBeyondWindow(backup, source); err != nil {
		t.Fatal("a backup showing the preview's runtime removed was refused", err)
	}
	recoverySet(operation, "runtime_outcome", "applied")
	recoverySet(operation, "runtime_plan_id", uuid.New())
	recoverySet(backup.Tables["environments"][1], "created_at", newest.CreatedAt.Add(time.Minute))
	if err := refuseRetiredBeyondWindow(backup, source); err != nil {
		t.Fatal("an environment created after the newest base was refused", err)
	}
}

// Recovery merges the newest base, and the base of a root still needing recovery only when the
// newest doesn't carry that root. A base it lists is set aside unread, an unlisted one is compared
// with it first, and a root still needing recovery keeps its own base for its runtime.
func TestRecoveryUsesTheNewestBaseAndSetsTheRestAside(t *testing.T) {
	backup, full, plans := windowTestChain(t, 3)
	newest := windowTestHistory(t, full, plans, 1)
	archives := map[uuid.UUID]portability.Archive{}
	base := func(a portability.Archive, captured time.Time) loadedBase {
		b := loadedBase{ID: uuid.New(), Parent: uuid.New(), CapturedAt: captured}
		a.CreatedAt = captured
		archives[b.Parent] = a
		return b
	}
	decode := func(parent uuid.UUID) (portability.Archive, error) { return archives[parent], nil }
	listed := base(backup, newest.CreatedAt.Add(-2*time.Hour))
	unlisted := base(backup, newest.CreatedAt.Add(-time.Hour))
	n := base(newest, newest.CreatedAt)
	n.Supersedes = map[uuid.UUID]uuid.UUID{listed.ID: listed.Parent}
	out := recoverySource{Bases: []loadedBase{listed, unlisted, n}}
	if err := chooseRecoveryBases(&out, decode); err != nil {
		t.Fatal(err)
	}
	if out.Origins.Newest.Parent != n.Parent || len(out.Origins.Values) != 1 || out.History != newest.Recovery || len(out.SetAside) != 2 {
		t.Fatal("recovery didn't merge only the newest base and set the others aside", out.SetAside)
	}
	for _, set := range out.SetAside {
		if set.Checkpoint == listed.ID && !strings.Contains(set.Reason, "superseded") || set.Checkpoint == unlisted.ID && !strings.Contains(set.Reason, "agrees") {
			t.Fatal("a set-aside base gave the wrong reason", set)
		}
	}

	t.Run("a resumed root keeps its base", func(t *testing.T) {
		out := recoverySource{Bases: []loadedBase{listed, unlisted, n}, Hosts: map[uuid.UUID]deployment.RecoveryEvidence{uuid.New(): {ActiveParent: listed.Parent}}}
		if err := chooseRecoveryBases(&out, decode); err != nil {
			t.Fatal(err)
		}
		if _, err := out.Origins.origin(plans[0].EnvironmentID, out.Origins.Values[1].Environments[plans[0].EnvironmentID], listed.Parent); err != nil || len(out.SetAside) != 1 {
			t.Fatal("a root a host still runs lost its own base", err)
		}
	})
	t.Run("an unlisted base below the window", func(t *testing.T) {
		short := windowTestHistory(t, full, plans, 2)
		m := base(short, newest.CreatedAt.Add(time.Hour))
		out := recoverySource{Bases: []loadedBase{unlisted, m}}
		err := chooseRecoveryBases(&out, decode)
		if err == nil || !strings.Contains(err.Error(), unlisted.ID.String()) || strings.Contains(err.Error(), "management backup") {
			t.Fatal("an unlisted checkpoint older than the window wasn't refused by name", err)
		}
	})
	t.Run("a forked management's root is merged", func(t *testing.T) {
		// A management restored from a backup ran other work in another environment. The newest
		// base, from the original, doesn't carry that root, so its own base is merged with it.
		other := appliedTestPlan()
		forkedArchive := appliedTestDeploy(backup, other, 1)
		forkedArchive.CreatedAt = newest.CreatedAt.Add(-time.Minute)
		root, _ := recoveryValue[uuid.UUID](forkedArchive.Tables["execution_operations"][1], "id")
		forked := loadedBase{ID: uuid.New(), Parent: root, CapturedAt: forkedArchive.CreatedAt}
		// Combining keys a runtime by its workspace and environment.
		for _, a := range []portability.Archive{newest, forkedArchive} {
			for _, row := range a.Tables["visual_runtime"] {
				recoverySet(row, "workspace_id", a.Workspace)
			}
		}
		archives[root] = forkedArchive
		out := recoverySource{Bases: []loadedBase{forked, n}, Hosts: map[uuid.UUID]deployment.RecoveryEvidence{uuid.New(): {ActiveParent: root}}}
		if err := chooseRecoveryBases(&out, decode); err != nil {
			t.Fatal(err)
		}
		merged := false
		for _, row := range out.Archive.Tables["execution_operations"] {
			if id, _ := recoveryValue[uuid.UUID](row, "id"); id == root {
				merged = true
			}
		}
		if !merged || !out.ForkedEnvironments[other.EnvironmentID] || out.Archive.Recovery != newest.Recovery || out.Archive.Version != newest.Version || len(out.Archive.Tables["environments"]) != 2 {
			t.Fatal("a root the newest base doesn't carry wasn't merged with it", merged, out.ForkedEnvironments)
		}
		// A root the newest base carries, as one it lists, is never merged from an older base.
		out = recoverySource{Bases: []loadedBase{listed, n}, Hosts: map[uuid.UUID]deployment.RecoveryEvidence{uuid.New(): {ActiveParent: listed.Parent}}}
		if err := chooseRecoveryBases(&out, decode); err != nil {
			t.Fatal(err)
		}
		if len(out.ForkedEnvironments) != 0 || len(out.Archive.Tables["execution_operations"]) != len(newest.Tables["execution_operations"]) {
			t.Fatal("an older base of the newest one's own lineage was merged")
		}
	})
	t.Run("an unlisted fork", func(t *testing.T) {
		p := plans[0]
		p.ID = uuid.New()
		p = p.WithHash()
		fork := base(appliedTestDeploy(backup, p, 2), newest.CreatedAt.Add(-time.Minute))
		out := recoverySource{Bases: []loadedBase{fork, n}}
		if err := chooseRecoveryBases(&out, decode); err == nil {
			t.Fatal("a diverged base was set aside")
		}
	})
}

// A cross-host root records its runtime while it runs, reports no sequence and can still end
// failed. Its ledger entry gives the same applied evidence as its row, so a backup whose head it
// is still matches a base that carries it only as an entry.
func TestLedgerEntryAgreesWithAFailedRootsRow(t *testing.T) {
	source := appliedTestGroup(t)
	restore := deployment.RestoreCutover{SourcePlanID: source.ID, SourceHash: source.Hash, PolicyID: uuid.New(), RunID: uuid.New(), ManifestHash: strings.Repeat("a", 64), OldVolumeID: source.Specification.Volumes[0].ID, TargetVolumeID: uuid.New()}
	target, err := deployment.RestoredGroup(source, restore, uuid.New(), uuid.New(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	body := coordinationCommand{Action: "maintenance", Plan: source, Maintenance: &coordinatedMaintenance{Restore: &target}}
	root := api.ExecutionCommand{Id: uuid.New(), EnvironmentId: source.EnvironmentID, ServerId: source.ServerID, ResourceId: source.UnitID, Kind: "crosshost", Generation: 10, PlanHash: Hash(body), Crosshost: rawMessage(mustJSON(body))}
	row := appliedEvidence{State: "failed", Runtime: portability.RuntimeOutcome{Kind: "applied", Plan: target.ID}}
	row.Applied, row.Outcome, row.PlanHash = committedApplied(root, row)
	if row.Applied.empty() {
		t.Fatal("the failed root's row has no applied runtime")
	}
	entry := portability.AppliedEntry{Environment: source.EnvironmentID, Operation: root.Id, Generation: 10, Identity: "v2:" + strings.Repeat("c", 64), Outcome: "applied", RuntimePlan: &target.ID, Revision: &target.RevisionID, PlanHash: target.Hash, State: "failed", RecordedAt: time.Now()}
	origin := &appliedOrigin{Operations: map[uuid.UUID]appliedEvidence{}, Starts: map[uuid.UUID]portability.RecoveryEnvironment{}}
	if err := addAppliedHistory(origin, &portability.RecoveryHistory{Applied: []portability.AppliedEntry{entry}}, map[uuid.UUID]map[int64]uuid.UUID{}); err != nil {
		t.Fatal(err)
	}
	got := origin.Operations[root.Id]
	if got.Applied != row.Applied || got.Outcome != row.Outcome || got.PlanHash != row.PlanHash {
		t.Fatal("the ledger entry and the row disagree on a failed root's runtime", got.Applied, row.Applied)
	}
}
