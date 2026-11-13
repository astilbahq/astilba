package portability

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecoveryArchiveVersion is the format recovery bases write: ArchiveVersion's tables and a recovery
// section, which an export never carries. A release before it refuses such a base, so cross-host
// recovery has to run on the release that sealed the newest base or a newer one.
const RecoveryArchiveVersion = 9

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
	raw, _ := json.Marshal(value)
	return raw
}

// The applied history a base carries: each carried environment's committed runtime changes, from
// rows and from entries an earlier recovery kept without their rows or outcomes
// (recovered_runtime_history).
// effective is when a change counts as recorded, less its margin.
// inline-sql: reads the base's transaction-local working set
const recoveryChanges = `WITH changes AS (
	SELECT o.environment_id AS env,o.id,o.generation,COALESCE(o.runtime_recorded_at,o.created_at) AS at,o.runtime_recorded_at IS NOT NULL AS exact,NULL::jsonb AS entry
	FROM execution_operations o WHERE o.workspace_id=$1 AND o.runtime_outcome IS NOT NULL AND o.environment_id IS NOT NULL
	UNION ALL SELECT h.environment_id,h.operation_id,h.generation,h.recorded_at,(h.entry->>'exact')::boolean,h.entry FROM recovered_runtime_history h
	WHERE h.workspace_id=$1 AND NOT EXISTS (SELECT 1 FROM execution_operations o WHERE o.workspace_id=$1 AND o.id=h.operation_id AND o.runtime_outcome IS NOT NULL)
), carried AS (
	SELECT c.*,c.at+CASE WHEN c.exact THEN interval '1 day' ELSE interval '7 days' END AS effective FROM changes c JOIN archive_keep k ON k.kind='env' AND k.id=c.env
)`

// Each carried environment's floor: the change it had when the window, starting at $2, opened.
// inline-sql: reads the base's transaction-local working set
const recoveryFloors = `, floors AS (SELECT env,max(generation) AS floor FROM carried WHERE effective<$2 GROUP BY env)`

// recoveryWindowStart moves start later until the ledger fits within limit entries beyond one per
// carried environment. A change whose effective time is before the start isn't in the window, and
// each environment adds at most the one it had when the window opened.
func recoveryWindowStart(ctx context.Context, tx pgx.Tx, ws uuid.UUID, start time.Time, limit int) (time.Time, error) {
	if limit < 1 {
		return time.Time{}, ErrArchive
	}
	var cut *time.Time
	// inline-sql: reads the base's transaction-local working set
	err := tx.QueryRow(ctx, recoveryChanges+` SELECT effective FROM carried WHERE effective>=$2 ORDER BY effective DESC OFFSET $3 LIMIT 1`, ws, start, limit-1).Scan(&cut)
	if errors.Is(err, pgx.ErrNoRows) {
		return start, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	// Past the limit-th newest: the window then holds at most limit-1 changes of its own.
	return cut.Add(time.Microsecond), nil
}

// recoveryLedger reads each carried environment's start and its applied history from it.
// identities holds command identities already computed, by operation, and gains the ones it
// computes, so a base sealed again reads each command once.
func recoveryLedger(ctx context.Context, tx pgx.Tx, ws uuid.UUID, start time.Time, identities map[uuid.UUID]string) ([]RecoveryEnvironment, []AppliedEntry, error) {
	environments := []RecoveryEnvironment{}
	// inline-sql: reads the base's transaction-local working set
	rows, err := tx.Query(ctx, recoveryChanges+recoveryFloors+` SELECT k.id,COALESCE(f.floor,0) FROM archive_keep k LEFT JOIN floors f ON f.env=k.id WHERE k.kind='env' ORDER BY k.id`, ws, start)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var e RecoveryEnvironment
		if err = rows.Scan(&e.Environment, &e.Floor); err != nil {
			rows.Close()
			return nil, nil, err
		}
		e.Whole = e.Floor == 0
		environments = append(environments, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	entries := []AppliedEntry{}
	// The applied revision and hash come from the plan stored for the runtime the change applied,
	// as recovery reads them for a carried row, so an entry and the row it stands for agree.
	// inline-sql: reads the base's transaction-local working set
	rows, err = tx.Query(ctx, recoveryChanges+recoveryFloors+`
	SELECT c.env,c.id,c.generation,c.at,c.exact,c.entry,(o.command->>'controllerGeneration')::bigint,o.state,o.report_sequence,o.runtime_outcome,o.runtime_plan_id,p.plan->>'id',p.plan->>'environmentId',p.plan->>'revisionId',p.plan->>'hash'
	FROM carried c LEFT JOIN floors f ON f.env=c.env LEFT JOIN execution_operations o ON c.entry IS NULL AND o.workspace_id=$1 AND o.id=c.id
	LEFT JOIN visual_plans p ON p.workspace_id=$1 AND p.id=o.runtime_plan_id
	WHERE c.generation>=COALESCE(f.floor,0)
	ORDER BY c.env,c.generation`, ws, start)
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
// the margin, and that the base carries neither as a row nor in its ledger. Before the ledger is
// read, roots alone bound the list: a runtime change that ended inside the window was recorded in
// it too, unless it ran across the window's start.
func recoverySettled(ctx context.Context, tx pgx.Tx, ws uuid.UUID, start time.Time, ledger []AppliedEntry, rootsOnly bool) ([]SettledOperation, error) {
	listed := []uuid.UUID{}
	for _, e := range ledger {
		listed = append(listed, e.Operation)
	}
	// inline-sql: reads the base's transaction-local working set
	rows, err := tx.Query(ctx, `SELECT o.id,o.state,o.report_sequence,o.runtime_outcome,o.runtime_plan_id,COALESCE(o.ended_at,o.created_at) FROM execution_operations o
	WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND (o.kind='crosshost' OR o.runtime_outcome IS NOT NULL) AND o.state IN ('succeeded','failed','cancelled')
	AND (o.ended_at>=$2 OR o.ended_at IS NULL AND o.created_at>=$3)
	AND NOT EXISTS (SELECT 1 FROM archive_keep k WHERE k.kind='op' AND k.id=o.id) AND NOT (o.id=ANY($4::uuid[])) AND NOT ($5 AND o.runtime_outcome IS NOT NULL)
	ORDER BY o.id`, ws, start.Add(-recoveryMargin), start.Add(-recoveryFallbackMargin), listed, rootsOnly)
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
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if err = q.KeepRecoveredRuntimeHistory(ctx, db.KeepRecoveredRuntimeHistoryParams{WorkspaceID: ws, OperationID: e.Operation, EnvironmentID: e.Environment, Generation: e.Generation, RecordedAt: e.RecordedAt, Entry: raw}); err != nil {
			return err
		}
	}
	return nil
}
