package execution

import (
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// recoveryRetirementFields are an environment's retirement, which recovery takes from a checkpoint
// as a whole or not at all.
var recoveryRetirementFields = []string{"state", "retired_at", "retired_by", "retirement_operation_id", "retired_without_host"}

// recoveryRetirementKnowledge takes the incoming environment's retirement when it is newer than what
// management knows. Its retirement operation orders it: management that has never seen that
// operation predates the retirement, as a database restored from before it does, and one that has
// seen it already holds that retirement or what came after, such as a restore or a later
// retirement. The same retirement only moves forward, from retiring to retired. A retirement that
// needed no operation, because nothing was deployed or its server was revoked, carries no such
// order, so what management knows stands.
func recoveryRetirementKnowledge(selected, prior, incoming portability.Row, operations map[string]portability.Row) error {
	retirement, err := recoveryValue[*uuid.UUID](incoming, "retirement_operation_id")
	if err != nil || retirement == nil {
		return err
	}
	current, err := recoveryValue[*uuid.UUID](prior, "retirement_operation_id")
	if err != nil {
		return err
	}
	take := false
	if current != nil && *current == *retirement {
		state, err := recoveryValue[string](prior, "state")
		if err != nil {
			return err
		}
		next, err := recoveryValue[string](incoming, "state")
		if err != nil {
			return err
		}
		take = state == "retiring" && next == "retired"
	} else {
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
