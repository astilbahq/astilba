package portability

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// RecoveryBases combines individually validated snapshots without retaining a
// decoded copy of every base. Capture time orders metadata, never host effects.
type RecoveryBases struct {
	archive  Archive
	rows     map[string]map[string]recoveryBaseRow
	metadata map[string]map[time.Time]string
	reports  map[string]map[int64]recoverySequenceStates
	// operations records which operations each base carries, by capture time.
	operations []recoveryBaseOperations
}

type recoveryBaseOperations struct {
	at   time.Time
	keys map[string]bool
}

type recoveryBaseRow struct {
	row Row
	at  time.Time
}

type recoverySequenceStates struct {
	live, terminal  api.ExecutionState
	terminalRemoved bool
}

func recoveryTerminal(state api.ExecutionState) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func (b *RecoveryBases) Add(a Archive) error {
	if a.Version < 5 || a.Version > ArchiveVersion || a.CreatedAt.IsZero() || b.rows != nil && b.archive.Workspace != a.Workspace {
		return ErrArchive
	}
	if b.rows == nil {
		b.rows = map[string]map[string]recoveryBaseRow{}
		b.metadata = map[string]map[time.Time]string{}
		b.reports = map[string]map[int64]recoverySequenceStates{}
	}
	workspace, _ := stablejson.Marshal(struct {
		Name string
		MFA  bool
	}{a.Name, a.RequireMFA})
	if err := b.observeMetadata("workspace", a.CreatedAt, workspace); err != nil {
		return err
	}
	if b.archive.CreatedAt.Before(a.CreatedAt) {
		b.archive = Archive{Version: a.Version, Workspace: a.Workspace, Name: a.Name, RequireMFA: a.RequireMFA, CreatedAt: a.CreatedAt}

	}
	carried := recoveryBaseOperations{at: a.CreatedAt, keys: map[string]bool{}}
	b.operations = append(b.operations, carried)
	for _, table := range tables {
		if b.rows[table.Name] == nil {
			b.rows[table.Name] = map[string]recoveryBaseRow{}
		}
		for _, row := range a.Tables[table.Name] {
			key, err := RecoveryRowKey(table.Name, row)
			if err != nil {
				return err
			}
			if table.Name == "execution_operations" {
				carried.keys[key] = true
			}
			incoming := recoveryBaseRow{row, a.CreatedAt}
			raw, _ := stablejson.Marshal(row)
			if table.Name == "execution_operations" {
				var sequence int64
				var state api.ExecutionState
				if json.Unmarshal(row["report_sequence"], &sequence) != nil || json.Unmarshal(row["state"], &state) != nil {
					return ErrArchive
				}
				if sequence > 0 {
					if b.reports[key] == nil {
						b.reports[key] = map[int64]recoverySequenceStates{}
					}
					seen := b.reports[key][sequence]
					prior := &seen.live
					if recoveryTerminal(state) {
						prior = &seen.terminal
					}
					outcome, err := ReadRuntimeOutcome(row)
					if err != nil {
						return err
					}
					if *prior != "" && *prior != state && !(recoveryTerminal(state) && seen.terminalRemoved && outcome.Kind == "removed") {
						return errors.New("recovery bases contain conflicting execution progress")
					}
					*prior = state
					if recoveryTerminal(state) {
						seen.terminalRemoved = outcome.Kind == "removed"
					}
					b.reports[key][sequence] = seen
				}
			} else if !RecoveryImmutable(table.Name) {
				if err := b.observeMetadata(table.Name+"/"+key, a.CreatedAt, raw); err != nil {
					return err
				}
			}
			known, exists := b.rows[table.Name][key]
			if !exists {
				b.rows[table.Name][key] = recoveryBaseRow{maps.Clone(row), a.CreatedAt}
				continue
			}
			prior, _ := stablejson.Marshal(known.row)
			if RecoveryImmutable(table.Name) {
				if recoveryRowHash(prior) != recoveryRowHash(raw) {
					return fmt.Errorf("recovery bases conflict on immutable %s", table.Name)
				}
				continue
			}
			selected := known
			if known.at.Before(a.CreatedAt) {
				selected = incoming
			}
			if table.Name == "execution_operations" {
				selected, err = combineRecoveryOperation(known, incoming)
				if err != nil {
					return err
				}
			}
			selected.row = maps.Clone(selected.row)
			b.rows[table.Name][key] = selected
		}
	}
	return nil
}

// Keep fingerprints of superseded metadata, so a later row cannot hide two
// contradictory copies of an earlier snapshot. Diagnostic steps are not receipts.
func (b *RecoveryBases) observeMetadata(key string, at time.Time, raw []byte) error {
	at = at.UTC()
	if b.metadata[key] == nil {
		b.metadata[key] = map[time.Time]string{}
	}
	hash := recoveryRowHash(raw)
	if prior, exists := b.metadata[key][at]; exists && prior != hash {
		return errors.New("simultaneous recovery bases contain conflicting metadata")
	}
	b.metadata[key][at] = hash
	return nil
}

func (b *RecoveryBases) Archive() Archive {
	out := b.archive
	out.Tables = map[string][]Row{}
	referenced := b.referencedOperations()
	for _, table := range tables {
		out.Tables[table.Name] = []Row{}
		keys := slices.Sorted(maps.Keys(b.rows[table.Name]))
		for _, key := range keys {
			selected := b.rows[table.Name][key]
			if table.Name == "execution_operations" && b.endedSince(key, selected, referenced) {
				continue
			}
			out.Tables[table.Name] = append(out.Tables[table.Name], selected.row)
		}
	}
	return out
}

// operationReferences are the archived columns that name an operation. A bounded base keeps every
// operation they name, as retainedOperations does.
var operationReferences = []struct{ table, column string }{
	{"environments", "runtime_operation_id"},
	{"execution_operations", "parent_id"},
	{"cross_host_operations", "id"},
	{"cross_host_approvals", "parent_id"},
	{"cross_host_reservations", "parent_id"},
	{"visual_requests", "operation_id"},
	{"pipeline_runs", "id"},
	{"pipeline_auto_deployments", "operation_id"},
	{"preview_requests", "build_id"},
	{"preview_requests", "operation_id"},
}

func (b *RecoveryBases) referencedOperations() map[string]bool {
	out := map[string]bool{}
	for _, ref := range operationReferences {
		for _, known := range b.rows[ref.table] {
			var id *string
			if json.Unmarshal(known.row[ref.column], &id) == nil && id != nil {
				key, _ := stablejson.Marshal([]string{*id})
				out[string(key)] = true
			}
		}
	}
	return out
}

// endedSince reports an unfinished copy that a later base contradicts. Every base keeps unfinished
// work, so an operation an earlier base holds unfinished and a later one leaves out had ended by
// then, and its last state is housekeeping history that bounded bases omit. Keeping the stale copy
// would merge it back as unfinished: two unfinished operations on one server or environment break
// their unique indexes, and an unfinished removal needs evidence a host may have pruned. An
// operation every base keeps whatever its state, a cross-host root, one that applied or removed a
// runtime, one another row names, a standalone removal, or a child of unfinished work, can only be
// missing from a base made on another history, so its copy stays.
func (b *RecoveryBases) endedSince(key string, selected recoveryBaseRow, referenced map[string]bool) bool {
	if !recoveryUnfinished(selected.row) || referenced[key] || recoveryOperationKept(selected.row) {
		return false
	}
	var parent *string
	if json.Unmarshal(selected.row["parent_id"], &parent) != nil {
		return false
	}
	if parent != nil {
		parentKey, _ := stablejson.Marshal([]string{*parent})
		if known, exists := b.rows["execution_operations"][string(parentKey)]; exists && recoveryUnfinished(known.row) {
			return false
		}
	}
	for _, later := range b.operations {
		if later.at.After(selected.at) && !later.keys[key] {
			return true
		}
	}
	return false
}

func recoveryUnfinished(row Row) bool {
	var state api.ExecutionState
	return json.Unmarshal(row["state"], &state) == nil && !recoveryTerminal(state)
}

// recoveryOperationKept matches what retainedOperations keeps whatever an operation's state.
func recoveryOperationKept(row Row) bool {
	var kind, outcome *string
	var parent *uuid.UUID
	var command struct {
		Network *struct {
			Action string `json:"action"`
		} `json:"network"`
	}
	if json.Unmarshal(row["kind"], &kind) != nil || json.Unmarshal(row["runtime_outcome"], &outcome) != nil || json.Unmarshal(row["parent_id"], &parent) != nil || json.Unmarshal(row["command"], &command) != nil || kind == nil {
		return true
	}
	return outcome != nil || parent == nil && (*kind == "crosshost" || *kind == "network" && command.Network != nil && command.Network.Action == "restrict")
}

func RecoveryRowKey(table string, row Row) (string, error) {
	values := []string{}
	for _, column := range strings.Split(recoveryKeys(table), ",") {
		value, err := rowString(row, column)
		if err != nil {
			return "", err
		}
		values = append(values, value)
	}
	raw, _ := stablejson.Marshal(values)
	return string(raw), nil
}

func combineRecoveryOperation(left, right recoveryBaseRow) (recoveryBaseRow, error) {
	a, b := left.row, right.row
	preferred := left
	if left.at.Before(right.at) || left.at.Equal(right.at) && recoveryOperationChoice(right.row) > recoveryOperationChoice(left.row) {
		preferred = right
	}
	conflict := errors.New("recovery bases contain conflicting execution progress")
	for _, field := range []string{"id", "workspace_id", "server_id", "environment_id", "generation", "plan_id", "request_key", "kind", "parent_id"} {
		if recoveryRowHash(a[field]) != recoveryRowHash(b[field]) {
			return recoveryBaseRow{}, conflict
		}
	}
	var ca, cb api.ExecutionCommand
	if json.Unmarshal(a["command"], &ca) != nil || json.Unmarshal(b["command"], &cb) != nil {
		return recoveryBaseRow{}, ErrArchive
	}
	ca.ControllerGeneration, cb.ControllerGeneration = nil, nil
	ca.RecoveryCheckpoint, cb.RecoveryCheckpoint = nil, nil
	ca.CancelRequested, cb.CancelRequested = false, false
	ra, _ := stablejson.Marshal(ca)
	rb, _ := stablejson.Marshal(cb)
	if recoveryRowHash(ra) != recoveryRowHash(rb) {
		return recoveryBaseRow{}, conflict
	}
	var sa, sb api.ExecutionState
	var qa, qb int64
	var cancelA, cancelB bool
	if json.Unmarshal(a["state"], &sa) != nil || json.Unmarshal(b["state"], &sb) != nil || json.Unmarshal(a["report_sequence"], &qa) != nil || json.Unmarshal(b["report_sequence"], &qb) != nil || json.Unmarshal(a["cancel_requested"], &cancelA) != nil || json.Unmarshal(b["cancel_requested"], &cancelB) != nil || qa < 0 || qb < 0 {
		return recoveryBaseRow{}, ErrArchive
	}
	outcome, sameRemoval, err := MergeRuntimeOutcomes(a, b)
	if err != nil {
		return recoveryBaseRow{}, err
	}
	if !sameRemoval && (recoveryTerminal(sa) && recoveryTerminal(sb) && sa != sb || recoveryTerminal(sa) && !recoveryTerminal(sb) && qb > qa || recoveryTerminal(sb) && !recoveryTerminal(sa) && qa > qb) {
		return recoveryBaseRow{}, conflict
	}
	selected := preferred
	if qa > qb || recoveryTerminal(sa) && !recoveryTerminal(sb) {
		selected = left
	} else if qb > qa || recoveryTerminal(sb) && !recoveryTerminal(sa) {
		selected = right
	}
	selected.row = maps.Clone(selected.row)
	setValue(selected.row, "cancel_requested", cancelA || cancelB)
	outcome.Set(selected.row)
	return selected, nil
}

func recoveryOperationChoice(row Row) string {
	copy := maps.Clone(row)
	delete(copy, "cancel_requested")
	raw, _ := stablejson.Marshal(copy)
	return recoveryRowHash(raw)
}
