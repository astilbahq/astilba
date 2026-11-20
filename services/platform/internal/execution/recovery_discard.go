package execution

import (
	"encoding/json/jsontext"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// recoveryDiscardKnowledge takes a restore's latest discard of its volume from a checkpoint when it
// is later than the one management knows. Each discard is an operation of the restore's
// environment, and creating one raised the environment's execution generation to its own, so of
// two discards the one with the higher generation is the later.
func recoveryDiscardKnowledge(selected, prior, incoming portability.Row, known, arriving map[string]portability.Row) error {
	next, err := recoveryValue[*uuid.UUID](incoming, "discard_operation_id")
	if err != nil {
		return err
	}
	current, err := recoveryValue[*uuid.UUID](prior, "discard_operation_id")
	if err != nil {
		return err
	}
	switch {
	case next == nil || current != nil && *current == *next:
		return nil
	case current == nil:
		selected["discard_operation_id"] = incoming["discard_operation_id"]
		return nil
	}
	held, err := recoveryOperationGeneration(prior["discard_operation_id"], known, arriving)
	if err != nil {
		return err
	}
	later, err := recoveryOperationGeneration(incoming["discard_operation_id"], known, arriving)
	if err != nil {
		return err
	}
	if later > held {
		selected["discard_operation_id"] = incoming["discard_operation_id"]
	}
	return nil
}

// recoveryOperationGeneration is the execution generation of the operation id names, from the first
// of operations that holds it. A restore's foreign keys keep the operations it names.
func recoveryOperationGeneration(id jsontext.Value, operations ...map[string]portability.Row) (int64, error) {
	key, err := portability.RecoveryRowKey("execution_operations", portability.Row{"id": id})
	if err != nil {
		return 0, err
	}
	for _, rows := range operations {
		if op, exists := rows[key]; exists {
			return recoveryValue[int64](op, "generation")
		}
	}
	return 0, portability.ErrArchive
}
