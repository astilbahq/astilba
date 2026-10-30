package portability

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// RuntimeOutcome records a committed projection change. Empty means unknown or
// no change; removed is an explicit outcome, independent of delivery state.
type RuntimeOutcome struct {
	Kind string
	Plan uuid.UUID
}

func ReadRuntimeOutcome(row Row) (RuntimeOutcome, error) {
	var kind *string
	var plan *uuid.UUID
	if raw := row["runtime_outcome"]; len(raw) != 0 && json.Unmarshal(raw, &kind) != nil {
		return RuntimeOutcome{}, ErrArchive
	}
	if raw := row["runtime_plan_id"]; len(raw) != 0 && json.Unmarshal(raw, &plan) != nil {
		return RuntimeOutcome{}, ErrArchive
	}
	out := RuntimeOutcome{}
	if kind != nil {
		out.Kind = *kind
	}
	if plan != nil {
		out.Plan = *plan
	}
	if kind == nil && plan == nil || out.Kind == "applied" && plan != nil && out.Plan != uuid.Nil || out.Kind == "removed" && plan == nil {
		return out, nil
	}
	return RuntimeOutcome{}, ErrArchive
}

func (o RuntimeOutcome) Set(row Row) {
	var kind *string
	var plan *uuid.UUID
	if o.Kind != "" {
		kind = &o.Kind
	}
	if o.Plan != uuid.Nil {
		plan = &o.Plan
	}
	setValue(row, "runtime_outcome", kind)
	setValue(row, "runtime_plan_id", plan)
}

// MergeRuntimeOutcomes never reconstructs an outcome from execution state. The
// boolean permits equivalent cleanup effects acknowledged by report or transfer.
func MergeRuntimeOutcomes(a, b Row) (RuntimeOutcome, bool, error) {
	x, err := ReadRuntimeOutcome(a)
	if err != nil {
		return x, false, err
	}
	y, err := ReadRuntimeOutcome(b)
	if err != nil {
		return x, false, err
	}
	if x.Kind != "" && y.Kind != "" && x != y {
		return x, false, errors.New("committed runtime outcomes conflict")
	}
	if x.Kind == "" {
		x = y
	}
	compatibleRemoval := x.Kind == "removed"
	if compatibleRemoval {
		for _, row := range []Row{a, b} {
			o, _ := ReadRuntimeOutcome(row)
			if o.Kind != "" {
				continue
			}
			var state string
			var sequence int64
			if json.Unmarshal(row["state"], &state) != nil || json.Unmarshal(row["report_sequence"], &sequence) != nil {
				return x, false, ErrArchive
			}
			// A fenced cleanup can acquire its authenticated removal receipt
			// later, without an agent report or a terminal-state transition.
			compatibleRemoval = state == "cancelled" && sequence == 0
		}
	}
	return x, compatibleRemoval, nil
}
