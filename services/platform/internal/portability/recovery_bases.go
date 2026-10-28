package portability

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// RecoveryBases combines individually validated snapshots without retaining a
// decoded copy of every base. Capture time orders metadata, never host effects.
type RecoveryBases struct {
	archive  Archive
	rows     map[string]map[string]recoveryBaseRow
	metadata map[string]map[time.Time]string
	reports  map[string]map[int64]recoverySequenceStates
}

type recoveryBaseRow struct {
	row Row
	at  time.Time
}

type recoverySequenceStates struct{ live, terminal api.ExecutionState }

func recoveryTerminal(state api.ExecutionState) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func (b *RecoveryBases) Add(a Archive) error {
	if a.Version != 5 || a.CreatedAt.IsZero() || b.rows != nil && b.archive.Workspace != a.Workspace {
		return ErrArchive
	}
	if b.rows == nil {
		b.rows = map[string]map[string]recoveryBaseRow{}
		b.metadata = map[string]map[time.Time]string{}
		b.reports = map[string]map[int64]recoverySequenceStates{}
	}
	workspace, _ := json.Marshal(struct {
		Name string
		MFA  bool
	}{a.Name, a.RequireMFA})
	if err := b.observeMetadata("workspace", a.CreatedAt, workspace); err != nil {
		return err
	}
	if b.archive.CreatedAt.Before(a.CreatedAt) {
		b.archive = Archive{Version: a.Version, Workspace: a.Workspace, Name: a.Name, RequireMFA: a.RequireMFA, CreatedAt: a.CreatedAt}

	}
	for _, table := range tables {
		if b.rows[table.Name] == nil {
			b.rows[table.Name] = map[string]recoveryBaseRow{}
		}
		for _, row := range a.Tables[table.Name] {
			key, err := RecoveryRowKey(table.Name, row)
			if err != nil {
				return err
			}
			incoming := recoveryBaseRow{row, a.CreatedAt}
			raw, _ := json.Marshal(row)
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
					if *prior != "" && *prior != state {
						return errors.New("recovery bases contain conflicting execution progress")
					}
					*prior = state
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
			prior, _ := json.Marshal(known.row)
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
	for _, table := range tables {
		out.Tables[table.Name] = []Row{}
		keys := slices.Sorted(maps.Keys(b.rows[table.Name]))
		for _, key := range keys {
			out.Tables[table.Name] = append(out.Tables[table.Name], b.rows[table.Name][key].row)
		}
	}
	return out
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
	raw, _ := json.Marshal(values)
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
	ra, _ := json.Marshal(ca)
	rb, _ := json.Marshal(cb)
	if recoveryRowHash(ra) != recoveryRowHash(rb) {
		return recoveryBaseRow{}, conflict
	}
	var sa, sb api.ExecutionState
	var qa, qb int64
	var cancelA, cancelB bool
	if json.Unmarshal(a["state"], &sa) != nil || json.Unmarshal(b["state"], &sb) != nil || json.Unmarshal(a["report_sequence"], &qa) != nil || json.Unmarshal(b["report_sequence"], &qb) != nil || json.Unmarshal(a["cancel_requested"], &cancelA) != nil || json.Unmarshal(b["cancel_requested"], &cancelB) != nil || qa < 0 || qb < 0 {
		return recoveryBaseRow{}, ErrArchive
	}
	if recoveryTerminal(sa) && recoveryTerminal(sb) && sa != sb || recoveryTerminal(sa) && !recoveryTerminal(sb) && qb > qa || recoveryTerminal(sb) && !recoveryTerminal(sa) && qa > qb {
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
	return selected, nil
}

func recoveryOperationChoice(row Row) string {
	copy := maps.Clone(row)
	delete(copy, "cancel_requested")
	raw, _ := json.Marshal(copy)
	return recoveryRowHash(raw)
}
