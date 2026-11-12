package execution

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/store"
)

// WithheldNote replaces an operation's note for a principal that may not read the environment.
const WithheldNote = "Details withheld: they can quote the environment's configuration, which needs environment:read."

// operationFor is an operation as a principal may see it. Its note and step details are free text
// from management, the host or the build runner, and a failure's quotes what failed: Compose and
// build output can carry image names, ports, paths and plaintext variable values. So, as with a
// plan, only a principal that may read the environment gets them. Any other gets WithheldNote in
// place of a note and no step details; the state, kind and step names stay, since they are what
// following an operation needs. Every exported function that returns a principal an api.Operation,
// or a list of them, returns it through here or operationsFor, which the operationview analyzer
// checks. The analyzer can't see an operation nested in another response, such as PipelineRuns'
// build history, which also holds build configuration and so is only for principals that may read.
func operationFor(p store.Principal, op api.Operation) api.Operation {
	if p.MayReadEnvironment() {
		return op
	}
	if op.Note != "" {
		op.Note = WithheldNote
	}
	for i := range op.Steps {
		op.Steps[i].Detail = ""
	}
	return op
}

// operationsFor is operationFor for each of a list of operations.
func operationsFor(p store.Principal, ops []api.Operation) []api.Operation {
	for i := range ops {
		ops[i] = operationFor(p, ops[i])
	}
	return ops
}
