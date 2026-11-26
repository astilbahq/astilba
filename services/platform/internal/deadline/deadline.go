// Package deadline says what a command's admission deadline means for it. The agent and management
// read the same rules: the agent ends a command its rule says must not start late, and management,
// whose clock decides, marks a command it delivers late as expired and sends one that changes
// nothing late without values.
package deadline

import (
	"encoding/json/v2"
	"errors"
	"iter"
	"maps"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// Rule is what a command's admission deadline means for it. The deadline decides whether a host may
// start something, never whether work it already holds can end. A backup or pipeline command a
// host started is bounded by its own lifetime from when it took it up, so time it waited for the
// host doesn't shorten it. Management's clock decides whether the deadline has passed: a command it
// delivers late is marked expired, whatever the host's clock says, and goes without values when its
// rule Lapses, so a command that reaches a host late, after the agent was silent or while its clock
// lags, never starts there. Every command kind, network action and coordinated phase that reaches
// the deadline has a rule below, so adding one means choosing one. Anything not listed keeps its
// deadline. Recovery opening and closing are decided before the deadline. See
// docs/cross-host-placement.md.
type Rule uint8

const (
	// StartsWork does not start after its deadline. Its parent treats the cancellation as a known
	// outcome: nothing started.
	StartsWork Rule = iota
	// OwnDeadline commands apply their deadline themselves. An upgrade changes nothing on its host
	// once its deadline has passed.
	OwnDeadline
	// NarrowsAccess only removes managed network access.
	NarrowsAccess
	// ExtendsHeldRoot adds a later checkpoint to a recovery root the host already holds.
	ExtendsHeldRoot
	// EndsHeldWork carries a reservation the host holds to its end. Nothing can issue it again, so
	// cancelling it would strand every host its parent reserved.
	EndsHeldWork
	// EndsHeldWorkOrNone is EndsHeldWork that also acknowledges a host holding no reservation at
	// all, without changing any reservation or runtime there. Refusing it would strand the other
	// hosts.
	EndsHeldWorkOrNone
	// RetiresHeldUnit removes what an environment's unit left on the host, and keeps its volumes. A
	// server that was offline when its environment was retired runs it on its return, when its
	// journal holds the unit: its applied plan, a release, its route set, or a deployment it ran,
	// even one that failed. A host whose journal holds none of these never ran the unit, so the
	// retirement has nothing to end there; its owner retires again.
	RetiresHeldUnit
	// BeforeAdmission is a coordinated recovery root's opening or closing, which the agent runs
	// before it looks at the deadline.
	BeforeAdmission
)

// Lapses reports whether a command with this rule changes nothing on its host once its deadline
// has passed, so that values delivered with it then could serve nothing.
func (r Rule) Lapses() bool { return r == StartsWork || r == OwnDeadline }

// kinds is every command kind the agent runs.
var kinds = map[api.ExecutionCommandKind]Rule{
	"test":      StartsWork,
	"update":    StartsWork, // Execute reconciles an artifact a timely update already installed.
	"visual":    StartsWork, // by action, below
	"pipeline":  StartsWork,
	"backup":    StartsWork,
	"schedule":  StartsWork,
	"upgrade":   OwnDeadline,
	"network":   StartsWork, // by action, below
	"crosshost": StartsWork, // by phase, below
	"restore":   StartsWork,
}

// visualActions is every action the agent's visual executor runs.
var visualActions = map[string]Rule{
	"resolve":         StartsWork,
	"observe":         StartsWork,
	"deploy":          StartsWork,
	"delete-volume":   StartsWork,
	"preview-cleanup": StartsWork,
	"retire":          RetiresHeldUnit,
	// A purge that arrives late fails, and its owner purges again: nothing else can run for a
	// retired environment meanwhile.
	"purge": StartsWork,
}

var networkActions = map[string]Rule{
	"identify":  StartsWork,
	"configure": StartsWork,
	"restrict":  NarrowsAccess,
}

var deploymentPhases = map[string]Rule{
	"checkpoint": ExtendsHeldRoot,
	"resolve":    StartsWork,
	"observe":    StartsWork,
	"reserve":    StartsWork,
	"prepare":    StartsWork,
	"hook":       StartsWork,
	"probe":      StartsWork,
	"publish":    StartsWork, // Its probes would be as old as it is.
	"retire":     EndsHeldWork,
	"complete":   EndsHeldWork,
	"abort":      EndsHeldWorkOrNone,
}

var maintenancePhases = map[string]Rule{
	"checkpoint":    ExtendsHeldRoot,
	"reserve":       StartsWork,
	"pause":         StartsWork,
	"effect":        StartsWork,
	"restore":       StartsWork, // It replaces data.
	"resume-source": EndsHeldWorkOrNone,
	"resume-target": EndsHeldWork,
	"complete":      EndsHeldWorkOrNone,
}

// Opening and closing a recovery root's metadata, for either kind of operation. The agent runs
// them before admission, so they have no rule in the tables.
const (
	RecoveryOpenPhase  = "recovery-open"
	RecoveryClosePhase = "checkpoint-closed"
)

// KnownKind reports whether the agent runs commands of this kind.
func KnownKind(kind api.ExecutionCommandKind) bool {
	_, known := kinds[kind]
	return known
}

// VisualActions is every visual action that has a rule.
func VisualActions() iter.Seq[string] { return maps.Keys(visualActions) }

// Of classifies a command from its kind and the action or phase its frozen body names. An unknown
// or invalid one keeps its deadline.
func Of(c api.ExecutionCommand) Rule {
	rule, ok := kinds[c.Kind]
	if !ok {
		return StartsWork
	}
	switch c.Kind {
	case "visual":
		var p deployment.Payload
		if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Digest() != c.PlanHash {
			return StartsWork
		}
		return visualActions[p.Action]
	case "network":
		var n hostnetwork.Command
		if c.Network == nil || hostnetwork.Decode(*c.Network, &n) != nil || n.Validate() != nil {
			return StartsWork
		}
		return networkActions[n.Action]
	case "crosshost":
		body, err := CrossHost(c)
		if err != nil {
			return StartsWork
		}
		if body.Phase == RecoveryOpenPhase || body.Phase == RecoveryClosePhase {
			return BeforeAdmission
		}
		return phases(body)[body.Phase]
	}
	return rule
}

// phases is every phase the agent runs for this kind of coordinated operation, other than opening
// and closing recovery.
func phases(body deployment.CrossHostCommand) map[string]Rule {
	if body.Maintenance != nil {
		return maintenancePhases
	}
	return deploymentPhases
}

// KnownPhase reports whether a coordinated phase has a rule for its kind of operation. Recovery
// opening and closing have none.
func KnownPhase(body deployment.CrossHostCommand) bool {
	_, known := phases(body)[body.Phase]
	return known
}

// RunsCoordinatedPhase says whether the agent runs a coordinated command naming this phase. When an
// integration test ends, its fixture checks every phase management issued against this, so a phase
// management issues in any tested flow fails until it is classified here.
func RunsCoordinatedPhase(body deployment.CrossHostCommand) bool {
	return body.Phase == RecoveryOpenPhase || body.Phase == RecoveryClosePhase || KnownPhase(body)
}

// CrossHost decodes a coordinated command's frozen body, which must name the command it came with.
func CrossHost(c api.ExecutionCommand) (deployment.CrossHostCommand, error) {
	var command deployment.CrossHostCommand
	if c.Crosshost == nil || hostnetwork.Decode(*c.Crosshost, &command) != nil || command.Digest() != c.PlanHash || c.ParentId == nil || *c.ParentId != command.ParentID || command.ParentID == uuid.Nil || len(command.GroupHash) != 64 || command.Unit.Plan.ServerID != c.ServerId || command.Unit.Plan.EnvironmentID != c.EnvironmentId || command.Unit.Plan.UnitID != c.ResourceId {
		return command, errors.New("invalid coordinated command identity")
	}
	return command, nil
}

// Passed reports whether a command's deadline has passed, by this clock or by management's, which
// marks a command it delivers late as expired. Management's clock decides: a host whose clock lags
// would otherwise start work after its deadline.
func Passed(c api.ExecutionCommand) bool {
	return c.Expired != nil && *c.Expired || time.Now().After(c.Deadline)
}
