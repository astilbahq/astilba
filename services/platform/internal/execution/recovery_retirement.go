package execution

import (
	"encoding/json/jsontext"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// recoveryRetirementFields are an environment's retirement and purge, which recovery takes from a
// checkpoint as a whole or not at all.
var recoveryRetirementFields = []string{"state", "retired_at", "retired_by", "retirement_operation_id", "retired_without_host", "purged_at", "purged_by", "purge_operation_id", "purged_without_host"}

// recoveryRetirementKnowledge takes the incoming environment's retirement when it is newer than what
// management knows, and is idempotent, because a taken retirement is then what management knows.
// A purge follows a retirement and is never undone, so recoveryPurgeKnowledge orders any record
// that holds one.
//
//   - Management holds a retirement operation and the checkpoint names the same one: the retirement
//     only moves forward, from retiring to retired.
//   - Management holds a retirement operation and the checkpoint names none or another: the
//     operation's generation orders them. Creating it raised the environment's execution
//     generation to its own, so a checkpoint whose generation has reached it has seen that
//     retirement and what came after, such as a restore, newer work or a later retirement. One
//     below it predates the retirement.
//   - Management holds no retirement operation and the checkpoint names one: management that has
//     never seen that operation predates the retirement, as a database restored from before it
//     does, and one that has seen it already holds that retirement or what came after.
//   - Neither names one: a retirement that needed no operation, because nothing was deployed or its
//     server was revoked, carries no order, so what management knows stands.
func recoveryRetirementKnowledge(selected, prior, incoming portability.Row, operations map[string]portability.Row) error {
	priorPurge, err := purgeRank(prior)
	if err != nil {
		return err
	}
	incomingPurge, err := purgeRank(incoming)
	if err != nil {
		return err
	}
	var take bool
	if priorPurge > 0 || incomingPurge > 0 {
		take, err = recoveryPurgeKnowledge(prior, incoming, priorPurge, incomingPurge, operations)
	} else {
		take, err = recoveryRetirementOrder(prior, incoming, operations)
	}
	if err != nil {
		return err
	}
	if take {
		for _, field := range recoveryRetirementFields {
			selected[field] = incoming[field]
		}
	}
	return nil
}

// recoverySecretKnowledge takes a checkpoint's retirement of a secret that management holds live. A
// retirement freed the secret's name and erased its values, so it is never undone.
func recoverySecretKnowledge(selected, prior, incoming portability.Row) {
	if string(prior["retired_at"]) == "null" {
		selected["retired_at"] = incoming["retired_at"]
	}
}

// purgeRank orders an environment's purge: 0 for none, 1 while purging, 2 once purged.
func purgeRank(row portability.Row) (int, error) {
	state, err := recoveryValue[string](row, "state")
	switch {
	case err != nil:
		return 0, err
	case state == "purging":
		return 1, nil
	case state == "purged":
		return 2, nil
	}
	return 0, nil
}

// recoveryPurgeKnowledge takes a checkpoint's purge whenever management's environment has not got
// as far. A purge erased the environment's secret values and is never undone, so management never
// takes back a retirement or an active environment over it.
//
//   - The checkpoint holds no purge: management keeps its own.
//   - Management holds none: the checkpoint's is taken.
//   - Both name different purge operations, as when a purge that failed was asked for again: the
//     operations' generations order them, as they order retirements.
//   - Otherwise the purge only moves forward, from purging to purged.
func recoveryPurgeKnowledge(prior, incoming portability.Row, priorRank, incomingRank int, operations map[string]portability.Row) (bool, error) {
	switch {
	case incomingRank == 0:
		return false, nil
	case priorRank == 0:
		return true, nil
	}
	current, err := recoveryValue[*uuid.UUID](prior, "purge_operation_id")
	if err != nil {
		return false, err
	}
	purge, err := recoveryValue[*uuid.UUID](incoming, "purge_operation_id")
	if err != nil {
		return false, err
	}
	if current != nil && purge != nil && *current != *purge {
		return recoveryGenerationReached(incoming, prior["purge_operation_id"], operations)
	}
	return incomingRank > priorRank, nil
}

// recoveryRetirementOrder is recoveryRetirementKnowledge for two environments neither of which
// holds a purge.
func recoveryRetirementOrder(prior, incoming portability.Row, operations map[string]portability.Row) (bool, error) {
	retirement, err := recoveryValue[*uuid.UUID](incoming, "retirement_operation_id")
	if err != nil {
		return false, err
	}
	current, err := recoveryValue[*uuid.UUID](prior, "retirement_operation_id")
	if err != nil {
		return false, err
	}
	switch {
	case current != nil && retirement != nil && *current == *retirement:
		state, err := recoveryValue[string](prior, "state")
		if err != nil {
			return false, err
		}
		next, err := recoveryValue[string](incoming, "state")
		if err != nil {
			return false, err
		}
		return state == "retiring" && next == "retired", nil
	case current != nil:
		return recoveryGenerationReached(incoming, prior["retirement_operation_id"], operations)
	case retirement != nil:
		key, err := portability.RecoveryRowKey("execution_operations", portability.Row{"id": incoming["retirement_operation_id"]})
		if err != nil {
			return false, err
		}
		_, seen := operations[key]
		return !seen, nil
	}
	return false, nil
}

// recoveryGenerationReached says whether the incoming environment has seen the operation that
// management's environment names: creating it raised the environment's execution generation to the
// operation's own. The environment's foreign keys keep the operation it names.
func recoveryGenerationReached(incoming portability.Row, operation jsontext.Value, operations map[string]portability.Row) (bool, error) {
	key, err := portability.RecoveryRowKey("execution_operations", portability.Row{"id": operation})
	if err != nil {
		return false, err
	}
	known, exists := operations[key]
	if !exists {
		return false, portability.ErrArchive
	}
	created, err := recoveryValue[int64](known, "generation")
	if err != nil {
		return false, err
	}
	generation, err := recoveryValue[int64](incoming, "execution_generation")
	if err != nil {
		return false, err
	}
	return generation >= created, nil
}
