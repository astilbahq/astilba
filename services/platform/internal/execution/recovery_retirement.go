package execution

import (
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// recoveryRetirementFields are an environment's retirement, which recovery takes from a checkpoint
// as a whole or not at all.
var recoveryRetirementFields = []string{"state", "retired_at", "retired_by", "retirement_operation_id", "retired_without_host"}

// recoveryRetirementKnowledge takes the incoming environment's retirement when it is newer than what
// management knows, and is idempotent, because a taken retirement is then what management knows.
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
	retirement, err := recoveryValue[*uuid.UUID](incoming, "retirement_operation_id")
	if err != nil {
		return err
	}
	current, err := recoveryValue[*uuid.UUID](prior, "retirement_operation_id")
	if err != nil {
		return err
	}
	take := false
	switch {
	case current != nil && retirement != nil && *current == *retirement:
		state, err := recoveryValue[string](prior, "state")
		if err != nil {
			return err
		}
		next, err := recoveryValue[string](incoming, "state")
		if err != nil {
			return err
		}
		take = state == "retiring" && next == "retired"
	case current != nil:
		key, err := portability.RecoveryRowKey("execution_operations", portability.Row{"id": prior["retirement_operation_id"]})
		if err != nil {
			return err
		}
		// The environment's retirement_operation_owner key keeps the operation it names.
		operation, known := operations[key]
		if !known {
			return portability.ErrArchive
		}
		retired, err := recoveryValue[int64](operation, "generation")
		if err != nil {
			return err
		}
		generation, err := recoveryValue[int64](incoming, "execution_generation")
		if err != nil {
			return err
		}
		take = generation >= retired
	case retirement != nil:
		key, err := portability.RecoveryRowKey("execution_operations", portability.Row{"id": incoming["retirement_operation_id"]})
		if err != nil {
			return err
		}
		_, seen := operations[key]
		take = !seen
	}
	if take {
		for _, field := range recoveryRetirementFields {
			selected[field] = incoming[field]
		}
	}
	return nil
}
