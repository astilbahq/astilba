package portability

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecoveryArchiveVersion is the format recovery bases write: ArchiveVersion's tables and a recovery
// section, which an export never carries. Exports and recovery bases share one sequence of versions,
// so when exports take a new version recovery bases take the one after it, and recoveryBaseTables
// says which export version's tables each recovery base version carries
// (TestExportVersionsStayBelowTheRecoveryBaseVersion). A release before it refuses such a base, so
// cross-host recovery has to run on the release that sealed the newest base or a newer one.
const RecoveryArchiveVersion = 12

// recoveryBaseTables maps each recovery base version to the export version whose tables it carries.
var recoveryBaseTables = map[int]int{10: 9, RecoveryArchiveVersion: ArchiveVersion}

const (
	// RecoveryWindowTarget is how far back a base's applied history reaches when it fits.
	RecoveryWindowTarget = 90 * 24 * time.Hour
	// RecoveryShortWindow is the backup retention the docs suggest. A window shorter than it is
	// shown to owners as a warning and raises the recovery_window alert.
	RecoveryShortWindow = 35 * 24 * time.Hour
	// A change recorded before the window opens can commit after it does. A recorded time is
	// read as up to a day early, and a creation time standing in for one as up to a week early.
	recoveryMargin         = 24 * time.Hour
	recoveryFallbackMargin = 7 * 24 * time.Hour
	// recoveryEntryBytes bounds one serialised ledger entry, for budgeting before reading them.
	recoveryEntryBytes = 640
)

// ShortRecoveryWindow reports whether a base captured at captured has a window shorter than the
// backup retention the docs suggest, so cross-host recovery can't use a backup that old. A base
// captured before windows has none, carries every applied change, and isn't short.
func ShortRecoveryWindow(captured time.Time, windowStart *time.Time) bool {
	return windowStart != nil && captured.Sub(*windowStart) < RecoveryShortWindow
}

// RecoveryHistory is a base's recovery section. A backup is compatible with the base when it was
// taken after WindowStart: every runtime change committed since then is in Applied, together with
// the change each environment had when the window opened, so the base holds the head of any such
// backup.
type RecoveryHistory struct {
	WindowStart time.Time `json:"windowStart"`
	TargetDays  int       `json:"targetDays"`
	// Environments are the environments the base carries, each with the generation its history
	// starts at.
	Environments []RecoveryEnvironment `json:"environments"`
	Applied      []AppliedEntry        `json:"applied"`
	// Settled holds the final state of the roots and runtime changes that ended inside the window
	// and that neither a carried row nor Applied describes.
	Settled []SettledOperation `json:"settled"`
	// Superseded names every base a host may still hold that this management captured or
	// recovered before this one, which this base vouches are its ancestors.
	Superseded []SupersededBase `json:"superseded"`
}

// RecoveryEnvironment is where a carried environment's applied history starts: Floor is the
// generation of the change it had when the window opened, and Whole says it had none, so its whole
// history is in the window.
type RecoveryEnvironment struct {
	Environment uuid.UUID `json:"environment"`
	Floor       int64     `json:"floor"`
	Whole       bool      `json:"whole"`
}

// AppliedEntry is the evidence recovery compares for one committed runtime change, in explicit
// fields, so a later release reads it the same way. Revision and PlanHash describe the applied plan
// when it reads back from the plan this management stored for it.
type AppliedEntry struct {
	Environment uuid.UUID  `json:"environment"`
	Operation   uuid.UUID  `json:"operation"`
	Generation  int64      `json:"generation"`
	Identity    string     `json:"identity"`
	Outcome     string     `json:"outcome"`
	RuntimePlan *uuid.UUID `json:"runtimePlan"`
	Revision    *uuid.UUID `json:"revision"`
	PlanHash    string     `json:"planHash"`
	State       string     `json:"state"`
	Sequence    int64      `json:"sequence"`
	Controller  int64      `json:"controller"`
	RecordedAt  time.Time  `json:"recordedAt"`
	// Exact says RecordedAt is when the change was recorded, not when its operation was created.
	Exact bool `json:"exact"`
}

// SettledOperation is the final state of an operation that ended inside the window.
type SettledOperation struct {
	Operation   uuid.UUID  `json:"operation"`
	State       string     `json:"state"`
	Sequence    int64      `json:"sequence"`
	Outcome     *string    `json:"outcome"`
	RuntimePlan *uuid.UUID `json:"runtimePlan"`
	EndedAt     time.Time  `json:"endedAt"`
}

// SupersededBase names a base by its checkpoint, root and capture time.
type SupersededBase struct {
	Base       uuid.UUID `json:"base"`
	Parent     uuid.UUID `json:"parent"`
	CapturedAt time.Time `json:"capturedAt"`
}

// Runtime is the entry's runtime outcome.
func (e AppliedEntry) Runtime() RuntimeOutcome {
	out := RuntimeOutcome{Kind: e.Outcome}
	if e.RuntimePlan != nil {
		out.Plan = *e.RuntimePlan
	}
	return out
}

// Records counts the section's entries against the archive's record limit.
func (h *RecoveryHistory) Records() int {
	if h == nil {
		return 0
	}
	return len(h.Environments) + len(h.Applied) + len(h.Settled) + len(h.Superseded)
}

func executionState(state string) bool {
	switch state {
	case "queued", "dispatched", "running", "needs_attention", "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

func identityValid(identity string) bool {
	digest, found := strings.CutPrefix(identity, "v2:")
	_, err := hex.DecodeString(digest)
	return found && len(digest) == 64 && err == nil && strings.ToLower(digest) == digest
}

// validateRecoveryHistory checks a decoded base's section against its tables: every carried
// environment has a start, every entry belongs to one at or above its start, the change each
// environment had when the window opened is present, and nothing is listed twice.
func validateRecoveryHistory(a Archive) error {
	h := a.Recovery
	if h.WindowStart.IsZero() || !h.WindowStart.Before(a.CreatedAt) || h.TargetDays < 1 || h.TargetDays > 3650 || h.Environments == nil || h.Applied == nil || h.Settled == nil || h.Superseded == nil {
		return ErrArchive
	}
	carried := map[uuid.UUID]bool{}
	for _, row := range a.Tables["environments"] {
		var id uuid.UUID
		if json.Unmarshal(row["id"], &id) != nil {
			return ErrArchive
		}
		carried[id] = true
	}
	floors := map[uuid.UUID]RecoveryEnvironment{}
	for _, e := range h.Environments {
		if _, duplicate := floors[e.Environment]; duplicate || !carried[e.Environment] || e.Floor < 0 || e.Whole != (e.Floor == 0) {
			return ErrArchive
		}
		floors[e.Environment] = e
	}
	if len(floors) != len(carried) {
		return ErrArchive
	}
	operations := map[uuid.UUID]bool{}
	generations := map[uuid.UUID]map[int64]bool{}
	for _, e := range h.Applied {
		start, known := floors[e.Environment]
		if !known || operations[e.Operation] || e.Operation == uuid.Nil || e.Generation < 1 || e.Generation < start.Floor || generations[e.Environment][e.Generation] || !identityValid(e.Identity) || !executionState(e.State) || e.Sequence < 0 || e.Controller < 0 || e.RecordedAt.IsZero() {
			return ErrArchive
		}
		switch e.Outcome {
		case "applied":
			if e.RuntimePlan == nil || *e.RuntimePlan == uuid.Nil || (e.Revision == nil) != (e.PlanHash == "") {
				return ErrArchive
			}
		case "removed":
			if e.RuntimePlan != nil || e.Revision != nil || e.PlanHash != "" {
				return ErrArchive
			}
		default:
			return ErrArchive
		}
		operations[e.Operation] = true
		if generations[e.Environment] == nil {
			generations[e.Environment] = map[int64]bool{}
		}
		generations[e.Environment][e.Generation] = true
	}
	for env, start := range floors {
		if !start.Whole && !generations[env][start.Floor] {
			return ErrArchive
		}
	}
	for _, s := range h.Settled {
		if operations[s.Operation] || s.Operation == uuid.Nil || s.State != "succeeded" && s.State != "failed" && s.State != "cancelled" || s.Sequence < 0 || s.EndedAt.IsZero() {
			return ErrArchive
		}
		if _, err := ReadRuntimeOutcome(Row{"runtime_outcome": marshalValue(s.Outcome), "runtime_plan_id": marshalValue(s.RuntimePlan)}); err != nil {
			return err
		}
		operations[s.Operation] = true
	}
	bases := map[uuid.UUID]bool{}
	for _, b := range h.Superseded {
		// A base a recovery registered keeps its own management's capture time, which can be later
		// than this one's, so only listing orders bases.
		if bases[b.Base] || b.Base == uuid.Nil || b.Parent == uuid.Nil || b.CapturedAt.IsZero() {
			return ErrArchive
		}
		bases[b.Base] = true
	}
	return nil
}

func marshalValue(value any) []byte {
	raw, _ := stablejson.Marshal(value)
	return raw
}

// The applied history a base carries is each carried environment's committed runtime changes,
// from rows and from entries an earlier recovery kept without their rows or outcomes
// (recovered_runtime_history), which count only where no row records the change. A change is
// recorded at its runtime_recorded_at, or its creation where it has none, and counts as recorded
// from then plus its margin: a day, or a week for a creation time. It ends at its ended_at plus a
// day, or its creation plus a week where it has none. An entry has no end.
//
// Each statement reads only the history near the window, so a capture's cost follows its window
// and not the workspace's age: by time through execution_operations_applied_times and
// recovered_runtime_history_recorded, or by environment and generation through
// execution_operations_applied_history and recovered_runtime_history_chain. Whether a row records an
// entry's change is its outcome, looked up by the entry's operation as a value: written as NOT
// EXISTS, the planner may instead hash every change the workspace has to compare them. Each runs
// as an unnamed statement, planned for its window every time: a cached statement turns generic
// after a few runs, and a generic plan, not knowing where the window starts, guesses it holds a
// third of the history and may read all of it.

// recoveryWindowItems are the window's items, starting at $2: each carried environment's changes
// that count as recorded or ended inside it, and the roots that ended in it that a base carries
// neither as a row nor in its ledger, which its settled list names.
// execution_operations_applied_times orders changes by the latest of their recorded and end
// times, each without its margin, so a change that counts inside the window is among those whose
// latest is at most the widest margin before it. That margin is a week of days in the session's
// time zone, so the bound is a day longer, in hours, to cover any clock change inside it. Roots
// with an end time and roots without one are read apart, so each is a range of
// execution_operations_settled: as one condition with OR, the planner reads every root the
// workspace has ended.
// inline-sql: reads the base's transaction-local working set
const recoveryWindowItems = `WITH items AS (
	SELECT GREATEST(COALESCE(o.runtime_recorded_at+interval '1 day',o.created_at+interval '7 days'),COALESCE(o.ended_at+interval '1 day',o.created_at+interval '7 days')) AS effective
	FROM execution_operations o JOIN archive_keep k ON k.kind='env' AND k.id=o.environment_id
	WHERE o.workspace_id=$1 AND o.runtime_outcome IS NOT NULL AND GREATEST(COALESCE(o.runtime_recorded_at,o.created_at),COALESCE(o.ended_at,o.created_at))>=$2::timestamptz-interval '192 hours'
	UNION ALL SELECT h.recorded_at+CASE WHEN (h.entry->>'exact')::boolean THEN interval '1 day' ELSE interval '7 days' END
	FROM recovered_runtime_history h JOIN archive_keep k ON k.kind='env' AND k.id=h.environment_id
	WHERE h.workspace_id=$1 AND h.recorded_at>=$2::timestamptz-interval '192 hours'
	AND (SELECT o.runtime_outcome FROM execution_operations o WHERE o.workspace_id=$1 AND o.id=h.operation_id) IS NULL
	UNION ALL SELECT o.ended_at+interval '1 day' FROM execution_operations o
	WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND o.kind='crosshost' AND o.runtime_outcome IS NULL AND o.state IN ('succeeded','failed','cancelled')
	AND o.ended_at>=$2::timestamptz-interval '1 day'
	AND NOT EXISTS (SELECT 1 FROM archive_keep k WHERE k.kind='op' AND k.id=o.id)
	UNION ALL SELECT o.created_at+interval '7 days' FROM execution_operations o
	WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND o.kind='crosshost' AND o.runtime_outcome IS NULL AND o.state IN ('succeeded','failed','cancelled')
	AND o.ended_at IS NULL AND o.created_at>=$2::timestamptz-interval '7 days'
	AND NOT EXISTS (SELECT 1 FROM archive_keep k WHERE k.kind='op' AND k.id=o.id)
) SELECT effective FROM items WHERE effective>=$2 ORDER BY effective DESC OFFSET $3 LIMIT 1`

// recoveryFloors is each carried environment's floor: the generation of the newest change it had
// counted as recorded when the window, starting at $2, opened, or 0 if it had none. Each is a
// lookup down the environment's changes from its newest, which passes only the changes counted
// inside the window: first its rows, then its entries above the floor the rows give. A recovery
// keeps an entry for every change its base carried, so most entries have rows, and below that
// floor they would all be passed without raising it.
// inline-sql: reads the base's transaction-local working set
const recoveryFloors = `WITH r AS MATERIALIZED (
	SELECT k.id,COALESCE((SELECT o.generation FROM execution_operations o WHERE o.workspace_id=$1 AND o.environment_id=k.id AND o.runtime_outcome IS NOT NULL
		AND COALESCE(o.runtime_recorded_at+interval '1 day',o.created_at+interval '7 days')<$2 ORDER BY o.generation DESC LIMIT 1),0) AS floor
	FROM archive_keep k WHERE k.kind='env'
) SELECT r.id,GREATEST(r.floor,(SELECT h.generation FROM recovered_runtime_history h WHERE h.workspace_id=$1 AND h.environment_id=r.id AND h.generation>r.floor
		AND h.recorded_at+CASE WHEN (h.entry->>'exact')::boolean THEN interval '1 day' ELSE interval '7 days' END<$2
		AND (SELECT o.runtime_outcome FROM execution_operations o WHERE o.workspace_id=$1 AND o.id=h.operation_id) IS NULL
		ORDER BY h.generation DESC LIMIT 1))
FROM r ORDER BY r.id`

// recoveryLedgerEntries is each environment's changes from its floor up, given as the arrays $2
// and $3, for the window starting at $4. The changes above an environment's floor all count as
// recorded inside the window, so they are among those execution_operations_applied_times and
// recovered_runtime_history_recorded hold from the bound recoveryWindowItems reads them by; the
// floor's own change is read by its environment and generation. Each part is a range of an index
// whichever way the planner joins it to the floors. The applied revision and hash come from the plan stored for the runtime the
// change applied, as recovery reads them for a carried row, so an entry and the row it stands for
// agree.
// inline-sql: reads the base's transaction-local working set
const recoveryLedgerEntries = `WITH f AS (SELECT * FROM unnest($2::uuid[],$3::bigint[]) f(env,floor)), changes AS (
	SELECT o.environment_id,o.id,o.generation,o.runtime_recorded_at,o.created_at,o.command,o.state,o.report_sequence,o.runtime_outcome,o.runtime_plan_id FROM f JOIN execution_operations o ON o.environment_id=f.env AND o.generation>f.floor
	WHERE o.workspace_id=$1 AND o.runtime_outcome IS NOT NULL AND GREATEST(COALESCE(o.runtime_recorded_at,o.created_at),COALESCE(o.ended_at,o.created_at))>=$4::timestamptz-interval '192 hours'
	UNION ALL SELECT o.environment_id,o.id,o.generation,o.runtime_recorded_at,o.created_at,o.command,o.state,o.report_sequence,o.runtime_outcome,o.runtime_plan_id FROM f JOIN execution_operations o ON o.workspace_id=$1 AND o.environment_id=f.env AND o.generation=f.floor AND o.runtime_outcome IS NOT NULL
), entries AS (
	SELECT h.environment_id,h.operation_id,h.generation,h.recorded_at,h.entry FROM f JOIN recovered_runtime_history h ON h.environment_id=f.env AND h.generation>f.floor
	WHERE h.workspace_id=$1 AND h.recorded_at>=$4::timestamptz-interval '192 hours'
	UNION ALL SELECT h.environment_id,h.operation_id,h.generation,h.recorded_at,h.entry FROM f JOIN recovered_runtime_history h ON h.workspace_id=$1 AND h.environment_id=f.env AND h.generation=f.floor
)
SELECT o.environment_id,o.id,o.generation,COALESCE(o.runtime_recorded_at,o.created_at),o.runtime_recorded_at IS NOT NULL,NULL::jsonb,
	(o.command->>'controllerGeneration')::bigint,o.state,o.report_sequence,o.runtime_outcome,o.runtime_plan_id,p.plan->>'id',p.plan->>'environmentId',p.plan->>'revisionId',p.plan->>'hash'
FROM changes o LEFT JOIN visual_plans p ON p.workspace_id=$1 AND p.id=o.runtime_plan_id
UNION ALL SELECT h.environment_id,h.operation_id,h.generation,h.recorded_at,(h.entry->>'exact')::boolean,h.entry,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL
FROM entries h WHERE (SELECT o.runtime_outcome FROM execution_operations o WHERE o.workspace_id=$1 AND o.id=h.operation_id) IS NULL
ORDER BY 1,3`

// recoveryWindowStart moves start later until the window holds fewer than limit items
// (recoveryWindowItems). Both kinds only shrink as the window starts later. A change counts from
// when it was recorded or, if later, when it ended, since one recorded before the window and
// ended inside it is settled; each environment adds at most the one it had when the window
// opened.
func recoveryWindowStart(ctx context.Context, tx pgx.Tx, ws uuid.UUID, start time.Time, limit int) (time.Time, error) {
	if limit < 1 {
		return time.Time{}, ErrArchive
	}
	var cut *time.Time
	err := tx.QueryRow(ctx, recoveryWindowItems, pgx.QueryExecModeDescribeExec, ws, start, limit-1).Scan(&cut)
	if errors.Is(err, pgx.ErrNoRows) {
		return start, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	// Past the limit-th newest: the window then holds at most limit-1 items.
	return cut.Add(time.Microsecond), nil
}

// recoveryLedger reads each carried environment's floor and its applied history from it.
// identities holds command identities already computed, by operation, and gains the ones it
// computes, so a base sealed again reads each command once.
func recoveryLedger(ctx context.Context, tx pgx.Tx, ws uuid.UUID, start time.Time, identities map[uuid.UUID]string) ([]RecoveryEnvironment, []AppliedEntry, error) {
	environments := []RecoveryEnvironment{}
	rows, err := tx.Query(ctx, recoveryFloors, pgx.QueryExecModeDescribeExec, ws, start)
	if err != nil {
		return nil, nil, err
	}
	ids, floors := []uuid.UUID{}, []int64{}
	for rows.Next() {
		var e RecoveryEnvironment
		if err = rows.Scan(&e.Environment, &e.Floor); err != nil {
			rows.Close()
			return nil, nil, err
		}
		e.Whole = e.Floor == 0
		environments = append(environments, e)
		ids, floors = append(ids, e.Environment), append(floors, e.Floor)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	entries := []AppliedEntry{}
	rows, err = tx.Query(ctx, recoveryLedgerEntries, pgx.QueryExecModeDescribeExec, ws, ids, floors, start)
	if err != nil {
		return nil, nil, err
	}
	missing := []uuid.UUID{}
	for rows.Next() {
		var e AppliedEntry
		var stored []byte
		var controller *int64
		var state, outcome *string
		var sequence *int64
		var plan *uuid.UUID
		var planID, planEnv, revision, hash *string
		if err = rows.Scan(&e.Environment, &e.Operation, &e.Generation, &e.RecordedAt, &e.Exact, &stored, &controller, &state, &sequence, &outcome, &plan, &planID, &planEnv, &revision, &hash); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if stored != nil {
			var kept AppliedEntry
			if json.Unmarshal(stored, &kept, json.RejectUnknownMembers(true)) != nil || kept.Operation != e.Operation || kept.Environment != e.Environment || kept.Generation != e.Generation {
				rows.Close()
				return nil, nil, ErrArchive
			}
			entries = append(entries, kept)
			continue
		}
		if state == nil || sequence == nil || outcome == nil {
			rows.Close()
			return nil, nil, ErrArchive
		}
		if controller != nil {
			e.Controller = *controller
		}
		e.State, e.Sequence, e.Outcome, e.RuntimePlan = *state, *sequence, *outcome, plan
		if e.Outcome == "applied" && plan != nil && planID != nil && *planID == plan.String() && planEnv != nil && *planEnv == e.Environment.String() && revision != nil && hash != nil && *hash != "" {
			id, err := uuid.Parse(*revision)
			if err == nil {
				e.Revision, e.PlanHash = &id, *hash
			}
		}
		if _, known := identities[e.Operation]; !known {
			missing = append(missing, e.Operation)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	if err = recoveryIdentities(ctx, tx, ws, missing, identities); err != nil {
		return nil, nil, err
	}
	for i := range entries {
		if entries[i].Identity == "" {
			entries[i].Identity = identities[entries[i].Operation]
		}
	}
	return environments, entries, nil
}

// recoveryIdentities computes the command identity of each operation in ids, from its stored
// command.
func recoveryIdentities(ctx context.Context, tx pgx.Tx, ws uuid.UUID, ids []uuid.UUID, identities map[uuid.UUID]string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := db.New(tx).OperationCommands(ctx, db.OperationCommandsParams{WorkspaceID: ws, Ids: ids})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if identities[row.ID], err = CommandIdentity(row.Command, CommandIdentityVersion); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if identities[id] == "" {
			return ErrArchive
		}
	}
	return nil
}

// recoverySettled reads the final state of roots and runtime changes that ended since start, less
// the margin, and that the base carries neither as a row nor in its ledger. Those with an end time
// and those without one are read apart, as recoveryWindowStart reads them.
func recoverySettled(ctx context.Context, tx pgx.Tx, ws uuid.UUID, start time.Time, ledger []AppliedEntry) ([]SettledOperation, error) {
	listed := []uuid.UUID{}
	for _, e := range ledger {
		listed = append(listed, e.Operation)
	}
	// inline-sql: reads the base's transaction-local working set
	rows, err := tx.Query(ctx, `SELECT o.id,o.state,o.report_sequence,o.runtime_outcome,o.runtime_plan_id,o.ended_at FROM execution_operations o
	WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND (o.kind='crosshost' OR o.runtime_outcome IS NOT NULL) AND o.state IN ('succeeded','failed','cancelled')
	AND o.ended_at>=$2
	AND NOT EXISTS (SELECT 1 FROM archive_keep k WHERE k.kind='op' AND k.id=o.id) AND NOT (o.id=ANY($4::uuid[]))
	UNION ALL SELECT o.id,o.state,o.report_sequence,o.runtime_outcome,o.runtime_plan_id,o.created_at FROM execution_operations o
	WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND (o.kind='crosshost' OR o.runtime_outcome IS NOT NULL) AND o.state IN ('succeeded','failed','cancelled')
	AND o.ended_at IS NULL AND o.created_at>=$3
	AND NOT EXISTS (SELECT 1 FROM archive_keep k WHERE k.kind='op' AND k.id=o.id) AND NOT (o.id=ANY($4::uuid[]))
	ORDER BY 1`, ws, start.Add(-recoveryMargin), start.Add(-recoveryFallbackMargin), listed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SettledOperation{}
	for rows.Next() {
		var s SettledOperation
		if err = rows.Scan(&s.Operation, &s.State, &s.Sequence, &s.Outcome, &s.RuntimePlan, &s.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func recoverySuperseded(ctx context.Context, tx pgx.Tx, ws uuid.UUID) ([]SupersededBase, error) {
	rows, err := db.New(tx).SupersededRecoveryBases(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := []SupersededBase{}
	for _, row := range rows {
		out = append(out, SupersededBase{Base: row.ID, Parent: row.ParentID, CapturedAt: row.CapturedAt})
	}
	return out, nil
}

// RecoveredHistory is the applied history an earlier recovery kept without its rows. A database's
// own origin reads it as a base's ledger: its runtime may have been recovered from one.
func RecoveredHistory(ctx context.Context, tx pgx.Tx, ws uuid.UUID) ([]AppliedEntry, error) {
	stored, err := db.New(tx).RecoveredRuntimeHistory(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := []AppliedEntry{}
	for _, raw := range stored {
		var e AppliedEntry
		if json.Unmarshal(raw, &e, json.RejectUnknownMembers(true)) != nil {
			return nil, ErrArchive
		}
		out = append(out, e)
	}
	return out, nil
}

// StoreRecoveredHistory keeps the newest base's applied history after a recovery merged it. A row
// takes the time its change was recorded, when the entry knows it; an entry whose row the merge
// didn't bring is kept as it is, so a later base carries it and a second recovery, from a backup
// taken inside this one's gap, still finds that backup's runtime.
func StoreRecoveredHistory(ctx context.Context, tx pgx.Tx, ws uuid.UUID, history *RecoveryHistory) error {
	if history == nil {
		return nil
	}
	q := db.New(tx)
	for _, e := range history.Applied {
		if e.Exact {
			if err := q.FillRecoveredRuntimeTime(ctx, db.FillRecoveredRuntimeTimeParams{RecordedAt: e.RecordedAt, WorkspaceID: ws, ID: e.Operation}); err != nil {
				return err
			}
		}
		raw, err := stablejson.Marshal(e)
		if err != nil {
			return err
		}
		if err = q.KeepRecoveredRuntimeHistory(ctx, db.KeepRecoveredRuntimeHistoryParams{WorkspaceID: ws, OperationID: e.Operation, EnvironmentID: e.Environment, Generation: e.Generation, RecordedAt: e.RecordedAt, Entry: raw}); err != nil {
			return err
		}
	}
	return nil
}
