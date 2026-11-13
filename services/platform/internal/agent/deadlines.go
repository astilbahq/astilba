package agent

import (
	"encoding/json/v2"
	"errors"
	"os"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// deadlineRule is what a command's admission deadline means for it. The deadline decides whether
// a host may start something, never whether work it already holds can end. Every command kind,
// network action and coordinated phase that reaches the deadline has a rule below, so adding one
// means choosing one. Anything not listed keeps its deadline. Recovery opening and closing are
// decided before the deadline, in Execute. See docs/cross-host-placement.md.
type deadlineRule uint8

const (
	// startsWork does not start after its deadline. Its parent treats the cancellation as a known
	// outcome: nothing started.
	startsWork deadlineRule = iota
	// ownDeadline commands apply their deadline themselves.
	ownDeadline
	// narrowsAccess only removes managed network access.
	narrowsAccess
	// extendsHeldRoot adds a later checkpoint to a recovery root this host already holds.
	extendsHeldRoot
	// endsHeldWork carries a reservation this host holds to its end. Nothing can issue it again,
	// so cancelling it would strand every host its parent reserved.
	endsHeldWork
	// endsHeldWorkOrNone is endsHeldWork that also acknowledges a host holding no reservation at
	// all, without changing any reservation or runtime there. Refusing it would strand the other hosts.
	endsHeldWorkOrNone
	// retiresHeldUnit removes what an environment's unit left on this host, and keeps its volumes.
	// A server that was offline when its environment was retired runs it on its return, when its
	// journal holds the unit: its applied plan, a release, its route set, or a deployment it ran,
	// even one that failed. A host whose journal holds none of these never ran the unit, so the
	// retirement has nothing to end there; its owner retires again.
	retiresHeldUnit
)

// commandKinds is every command kind the agent runs.
var commandKinds = map[api.ExecutionCommandKind]deadlineRule{
	"test":      startsWork,
	"update":    startsWork, // Execute reconciles an artifact a timely update already installed.
	"visual":    startsWork, // by action, below
	"pipeline":  startsWork,
	"backup":    startsWork,
	"schedule":  startsWork,
	"upgrade":   ownDeadline,
	"network":   startsWork, // by action, below
	"crosshost": startsWork, // by phase, below
}

// visualActions is every action executeVisual runs.
var visualActions = map[string]deadlineRule{
	"resolve":         startsWork,
	"observe":         startsWork,
	"deploy":          startsWork,
	"delete-volume":   startsWork,
	"preview-cleanup": startsWork,
	"retire":          retiresHeldUnit,
}

var networkActions = map[string]deadlineRule{
	"identify":  startsWork,
	"configure": startsWork,
	"restrict":  narrowsAccess,
}

var deploymentPhases = map[string]deadlineRule{
	"checkpoint": extendsHeldRoot,
	"resolve":    startsWork,
	"observe":    startsWork,
	"reserve":    startsWork,
	"prepare":    startsWork,
	"hook":       startsWork,
	"probe":      startsWork,
	"publish":    startsWork, // Its probes would be as old as it is.
	"retire":     endsHeldWork,
	"complete":   endsHeldWork,
	"abort":      endsHeldWorkOrNone,
}

var maintenancePhases = map[string]deadlineRule{
	"checkpoint":    extendsHeldRoot,
	"reserve":       startsWork,
	"pause":         startsWork,
	"effect":        startsWork,
	"restore":       startsWork, // It replaces data.
	"resume-source": endsHeldWorkOrNone,
	"resume-target": endsHeldWork,
	"complete":      endsHeldWorkOrNone,
}

// commandDeadline classifies a command from its kind and the action or phase its frozen body
// names. An unknown or invalid one keeps its deadline.
func commandDeadline(c api.ExecutionCommand) deadlineRule {
	rule, ok := commandKinds[c.Kind]
	if !ok {
		return startsWork
	}
	switch c.Kind {
	case "visual":
		var p deployment.Payload
		if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Digest() != c.PlanHash {
			return startsWork
		}
		return visualActions[p.Action]
	case "network":
		var n hostnetwork.Command
		if c.Network == nil || hostnetwork.Decode(*c.Network, &n) != nil || n.Validate() != nil {
			return startsWork
		}
		return networkActions[n.Action]
	case "crosshost":
		body, err := crossHostCommand(c)
		if err != nil {
			return startsWork
		}
		return crossHostPhases(body)[body.Phase]
	}
	return rule
}

// crossHostPhases is every phase the agent runs for this kind of coordinated operation, other than
// opening and closing recovery.
func crossHostPhases(body deployment.CrossHostCommand) map[string]deadlineRule {
	if body.Maintenance != nil {
		return maintenancePhases
	}
	return deploymentPhases
}

// Opening and closing a recovery root's metadata, for either kind of operation. Execute runs them
// before admission (recoveryOpen, recoveryClose), so they have no deadline rule.
const (
	recoveryOpenPhase  = "recovery-open"
	recoveryClosePhase = "checkpoint-closed"
)

// RunsCoordinatedPhase says whether the agent runs a coordinated command naming this phase. When
// an integration test ends, its fixture checks every phase management issued against this, so a
// phase management issues in any tested flow fails until it is classified here.
func RunsCoordinatedPhase(body deployment.CrossHostCommand) bool {
	if body.Phase == recoveryOpenPhase || body.Phase == recoveryClosePhase {
		return true
	}
	_, known := crossHostPhases(body)[body.Phase]
	return known
}

// runsLate says whether a command first taken after its admission deadline still runs. Its rule
// must allow it, and the work it ends must be what this host's journal holds: the command's claim
// is never enough. While a reservation is held, Accept has already refused any coordinated command
// that doesn't match it exactly (crossHostAdmission), so holding one here means holding this one. A command that runs late changes nothing beyond what its executor allows for
// the held work, and starts nothing.
func (e *Engine) runsLate(c api.ExecutionCommand) (bool, error) {
	rule := commandDeadline(c)
	switch rule {
	case narrowsAccess:
		return true, nil
	case extendsHeldRoot:
		d, err := commandCheckpoint(c)
		if err != nil || d.Sequence == 0 {
			return false, nil
		}
		head, err := e.Journal.recoveryHead(d.ParentID)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil && head.Descriptor.ParentID != uuid.Nil && deployment.SameRecoveryRoot(head.Descriptor, d), err
	case endsHeldWork, endsHeldWorkOrNone:
		held, err := e.Journal.crossHost()
		if err != nil {
			return false, err
		}
		return held.ParentID != uuid.Nil || rule == endsHeldWorkOrNone, nil
	case retiresHeldUnit:
		return e.Journal.holdsUnit(c)
	}
	return false, nil
}
