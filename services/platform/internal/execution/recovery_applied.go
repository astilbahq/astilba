package execution

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
)

var errAppliedProvenance = errors.New("applied runtime provenance is unknown; complete a fresh successful deployment and capture a new recovery checkpoint")

type appliedTuple struct{ Revision, Plan, Operation uuid.UUID }

func (a appliedTuple) empty() bool { return a == (appliedTuple{}) }

type appliedEvidence struct {
	Identity, Outcome, PlanHash      string
	Generation, Controller, Sequence int64
	State                            api.ExecutionState
	Runtime                          portability.RuntimeOutcome
	Applied                          appliedTuple
	Environment                      uuid.UUID
	// Recorded is when the change was recorded, or for a row, when its operation was created.
	Recorded time.Time
}

// Origins retain bounded evidence from each authenticated base independently.
// Combining rows first would let separate histories manufacture an ancestor.
type appliedOrigin struct {
	Parent       uuid.UUID
	At           time.Time
	Operations   map[uuid.UUID]appliedEvidence
	Environments map[uuid.UUID]appliedTuple
	Names        map[uuid.UUID]string
	// Window is a base's recovery section, which says where each environment's applied history
	// starts, or nil when the origin holds every change, as a database or an older base does.
	Window *portability.RecoveryHistory
	Starts map[uuid.UUID]portability.RecoveryEnvironment
}

// appliedOrigins are the origins recovery reads: the newest base, and the bases of roots it
// resumes, each compared with the newest when it was loaded.
type appliedOrigins struct {
	Values  []*appliedOrigin
	Newest  *appliedOrigin
	Records int
}

const maxAppliedOriginRecords = 100000

func appliedFrom(a portability.Archive, env uuid.UUID) (appliedTuple, error) {
	var out appliedTuple
	for _, row := range a.Tables["environments"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return out, err
		}
		if id != env {
			continue
		}
		var revision, operation *uuid.UUID
		if json.Unmarshal(row["applied_revision_id"], &revision) != nil {
			return out, portability.ErrArchive
		}
		if raw, exists := row["runtime_operation_id"]; exists && json.Unmarshal(raw, &operation) != nil {
			return out, portability.ErrArchive
		}
		if revision != nil {
			out.Revision = *revision
		}
		if operation != nil {
			out.Operation = *operation
		}
	}
	plan, err := recoveryRuntimePlan(a, env)
	out.Plan = plan
	if err != nil {
		return out, err
	}
	if (out.Revision == uuid.Nil) != (out.Plan == uuid.Nil) && out.Operation != uuid.Nil {
		return out, errors.New("applied runtime tuple is incomplete")
	}
	return out, nil
}

func buildAppliedOrigin(a portability.Archive, parent uuid.UUID) (*appliedOrigin, error) {
	out := &appliedOrigin{Parent: parent, At: a.CreatedAt, Operations: map[uuid.UUID]appliedEvidence{}, Environments: map[uuid.UUID]appliedTuple{}, Names: map[uuid.UUID]string{}, Starts: map[uuid.UUID]portability.RecoveryEnvironment{}}
	operations := map[uuid.UUID]portability.Row{}
	plans := map[uuid.UUID]jsontext.Value{}
	generations := map[uuid.UUID]map[int64]uuid.UUID{}
	for _, row := range a.Tables["execution_operations"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return nil, err
		}
		operations[id] = row
	}
	for _, row := range a.Tables["visual_plans"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return nil, err
		}
		plans[id] = row["plan"]
	}
	for id, row := range operations {
		var c api.ExecutionCommand
		if json.Unmarshal(row["command"], &c) != nil {
			return nil, portability.ErrArchive
		}
		operationGeneration, err := recoveryValue[int64](row, "generation")
		if err != nil {
			return nil, err
		}
		sequence, err := recoveryValue[int64](row, "report_sequence")
		if err != nil {
			return nil, err
		}
		state, err := recoveryValue[api.ExecutionState](row, "state")
		if err != nil {
			return nil, err
		}
		switch state {
		case "queued", "dispatched", "running", "needs_attention", "succeeded", "failed", "cancelled":
		default:
			return nil, portability.ErrArchive
		}
		identity, err := portability.CommandIdentity(row["command"], portability.CommandIdentityVersion)
		if err != nil {
			return nil, portability.ErrArchive
		}
		// Only a refusal's message reads it, so a row without one still compares.
		created, _ := recoveryValue[time.Time](row, "created_at")
		e := appliedEvidence{Identity: identity, Generation: operationGeneration, Controller: generation(c.ControllerGeneration), Sequence: sequence, State: state, Recorded: created}
		if sequence < 0 || e.Controller < 0 {
			return nil, portability.ErrArchive
		}
		workspace, err := recoveryValue[uuid.UUID](row, "workspace_id")
		if err != nil {
			return nil, err
		}
		env, err := recoveryValue[*uuid.UUID](row, "environment_id")
		if err != nil {
			return nil, err
		}
		server, err := recoveryValue[uuid.UUID](row, "server_id")
		if err != nil {
			return nil, err
		}
		kind, err := recoveryValue[api.ExecutionCommandKind](row, "kind")
		if err != nil {
			return nil, err
		}
		parentID, err := recoveryValue[*uuid.UUID](row, "parent_id")
		if err != nil {
			return nil, err
		}
		if env != nil {
			e.Environment = *env
		}
		if workspace == a.Workspace && env != nil && c.Id == id && c.ServerId == server && c.Kind == kind && sameID(c.ParentId, parentID) && c.EnvironmentId == *env && c.Generation == operationGeneration && operationGeneration > 0 {
			if c.Kind != "network" {
				if generations[*env] == nil {
					generations[*env] = map[int64]uuid.UUID{}
				}
				if prior, exists := generations[*env][operationGeneration]; exists && prior != id {
					return nil, errors.New("equal-generation operation identities conflict")
				}
				generations[*env][operationGeneration] = id
			}
			e.Runtime, err = portability.ReadRuntimeOutcome(row)
			if err != nil {
				return nil, err
			}
			e.Applied, e.Outcome, e.PlanHash = committedApplied(c, e)
			if e.Runtime.Kind != "" && e.Applied.empty() {
				return nil, errors.New("invalid committed runtime outcome")
			}
			if e.Applied.Plan != uuid.Nil {
				var plan deployment.Plan
				if json.Unmarshal(plans[e.Applied.Plan], &plan) != nil || plan.Validate(true) != nil || plan.ID != e.Applied.Plan || plan.RevisionID != e.Applied.Revision || plan.EnvironmentID != c.EnvironmentId || plan.Hash != e.PlanHash {
					e.Applied, e.Outcome = appliedTuple{}, ""
				}
			}
		}
		out.Operations[id] = e
	}
	if err := addAppliedHistory(out, a.Recovery, generations); err != nil {
		return nil, err
	}
	for _, row := range a.Tables["environments"] {
		env, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return nil, err
		}
		if name, err := recoveryValue[string](row, "name"); err == nil {
			out.Names[env] = name
		}
		tuple, err := appliedFrom(a, env)
		if err != nil {
			return nil, err
		}
		if tuple.Operation != uuid.Nil {
			e, exists := out.Operations[tuple.Operation]
			if !exists || e.Applied != tuple || e.Outcome == "" {
				return nil, errors.New("applied runtime does not match its committed operation")
			}
		}
		out.Environments[env] = tuple
	}
	return out, nil
}

// addAppliedHistory adds the applied history a base carries without the rows. A carried row's own
// evidence stands; an entry stands for the row the base left out, or for one that holds no outcome
// because recovery couldn't name its plan. The entry's applied plan was
// read back from the plan management stored when it captured the base, as buildAppliedOrigin reads
// a row's, so an entry and its row agree wherever the row's applied runtime is valid.
func addAppliedHistory(out *appliedOrigin, history *portability.RecoveryHistory, generations map[uuid.UUID]map[int64]uuid.UUID) error {
	if history == nil {
		return nil
	}
	if !history.WindowStart.IsZero() {
		out.Window = history
		for _, start := range history.Environments {
			out.Starts[start.Environment] = start
		}
	}
	for _, entry := range history.Applied {
		if row, carried := out.Operations[entry.Operation]; carried && row.Runtime.Kind != "" {
			continue
		}
		if generations[entry.Environment] == nil {
			generations[entry.Environment] = map[int64]uuid.UUID{}
		}
		if prior, exists := generations[entry.Environment][entry.Generation]; exists && prior != entry.Operation {
			return errors.New("equal-generation operation identities conflict")
		}
		generations[entry.Environment][entry.Generation] = entry.Operation
		e := appliedEvidence{Identity: entry.Identity, Generation: entry.Generation, Controller: entry.Controller, Sequence: entry.Sequence, State: api.ExecutionState(entry.State), Runtime: entry.Runtime(), Environment: entry.Environment, Recorded: entry.RecordedAt}
		switch {
		case entry.Outcome == "removed" && Terminal(e.State):
			e.Applied, e.Outcome = appliedTuple{Operation: entry.Operation}, Hash(e.Runtime)
		// A cross-host root records its runtime while it runs, never reports a sequence, and can
		// still end failed, so any ended change with a plan that reads back stands, as for a row.
		case entry.Outcome == "applied" && entry.Revision != nil && Terminal(e.State):
			e.Applied, e.Outcome, e.PlanHash = appliedTuple{*entry.Revision, *entry.RuntimePlan, entry.Operation}, Hash(e.Runtime), entry.PlanHash
		}
		out.Operations[entry.Operation] = e
	}
	return nil
}

// beyondWindow reports an older head the origin can't hold because it is older than the origin's
// window: below where the history of its environment starts.
func (o *appliedOrigin) beyondWindow(old appliedEvidence, names map[uuid.UUID]string) error {
	if o.Window == nil {
		return nil
	}
	start, carried := o.Starts[old.Environment]
	if !carried || start.Whole || old.Generation >= start.Floor {
		return nil
	}
	name := names[old.Environment]
	if name == "" {
		name = o.Names[old.Environment]
	}
	return recoveryWindowError{Environment: name, Since: o.Window.WindowStart, Recorded: old.Recorded}
}

func committedApplied(c api.ExecutionCommand, e appliedEvidence) (appliedTuple, string, string) {
	if c.ParentId != nil || e.Runtime.Kind == "" {
		return appliedTuple{}, "", ""
	}
	var plans []deployment.Plan
	switch c.Kind {
	case "visual":
		var body deployment.Payload
		if c.Visual == nil || json.Unmarshal(*c.Visual, &body) != nil || body.Digest() != c.PlanHash || c.ResourceId != body.Plan.UnitID || body.Plan.ServerID != c.ServerId || body.Plan.EnvironmentID != c.EnvironmentId {
			return appliedTuple{}, "", ""
		}
		if e.Runtime.Kind == "removed" {
			cleanup := body.Action == "preview-cleanup" && body.Plan.Preview != nil
			retirement := body.Action == retireAction && body.Plan.Preview == nil
			if !cleanup && !retirement || body.Plan.Validate(false) != nil || !Terminal(e.State) {
				return appliedTuple{}, "", ""
			}
			tuple := appliedTuple{Operation: c.Id}
			return tuple, Hash(e.Runtime), ""
		}
		if body.Action != "deploy" || e.State != "succeeded" || e.Sequence < 1 {
			return appliedTuple{}, "", ""
		}
		plans = append(plans, body.Plan)
	case "upgrade":
		var body upgrade.Command
		if e.State != "succeeded" || e.Sequence < 1 || c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &body) != nil || body.Digest() != c.PlanHash || body.Plan.Validate() != nil || c.ResourceId != body.Plan.ID || body.Plan.Source.ServerID != c.ServerId || body.Plan.Source.EnvironmentID != c.EnvironmentId {
			return appliedTuple{}, "", ""
		}
		plans = append(plans, body.Plan.TargetGroup(), body.Plan.Source)
	case "crosshost":
		var body coordinationCommand
		if !Terminal(e.State) || c.Crosshost == nil || json.Unmarshal(*c.Crosshost, &body) != nil || Hash(body) != c.PlanHash || body.Plan.Validate(true) != nil || c.ServerId != body.Plan.ServerID || c.ResourceId != body.Plan.UnitID {
			return appliedTuple{}, "", ""
		}
		if body.Maintenance == nil {
			if body.Action != "deploy" {
				return appliedTuple{}, "", ""
			}
			plans = append(plans, body.Plan)
		} else {
			if body.Maintenance.Restore != nil {
				plans = append(plans, *body.Maintenance.Restore)
			}
			if body.Maintenance.Effect.Upgrade != nil {
				if body.Maintenance.Effect.Upgrade.Plan.Validate() != nil {
					return appliedTuple{}, "", ""
				}
				plans = append(plans, body.Maintenance.Effect.Upgrade.Plan.TargetGroup())
			}
		}
	default:
		return appliedTuple{}, "", ""
	}
	if e.Runtime.Kind != "applied" {
		return appliedTuple{}, "", ""
	}
	for _, p := range plans {
		if p.ID == e.Runtime.Plan && p.Validate(true) == nil && p.EnvironmentID == c.EnvironmentId {
			tuple := appliedTuple{p.RevisionID, p.ID, c.Id}
			return tuple, Hash(e.Runtime), p.Hash
		}
	}
	return appliedTuple{}, "", ""
}

// compareApplied orders actual committed applications, not reads or prepares.
func compareApplied(candidate *appliedOrigin, next appliedTuple, current *appliedOrigin, old appliedTuple) (int, error) {
	if next == old {
		if next.Operation != uuid.Nil {
			a, b := candidate.Operations[next.Operation], current.Operations[old.Operation]
			if a.Identity != b.Identity || a.Outcome != b.Outcome || a.PlanHash != b.PlanHash || a.Runtime != b.Runtime || a.Runtime.Kind != "removed" && a.State != b.State || a.Generation != b.Generation {
				return 0, errors.New("applied operation outcomes conflict")
			}
		}
		return 0, nil
	}
	if next.empty() {
		return -1, nil
	}
	if next.Operation == uuid.Nil || !old.empty() && old.Operation == uuid.Nil {
		return 0, errAppliedProvenance
	}
	if old.empty() {
		return 1, nil
	}
	a, b := candidate.Operations[next.Operation], current.Operations[old.Operation]
	if a.Generation == b.Generation {
		return 0, errors.New("equal-generation applied operations conflict")
	}
	if a.Generation < b.Generation {
		order, err := compareApplied(current, old, candidate, next)
		return -order, err
	}
	ancestor, exists := candidate.Operations[old.Operation]
	if !exists {
		if err := candidate.beyondWindow(b, current.Names); err != nil {
			return 0, err
		}
	}
	if !exists || ancestor.Applied != old || ancestor.Identity != b.Identity || ancestor.Outcome != b.Outcome || ancestor.PlanHash != b.PlanHash || ancestor.Runtime != b.Runtime || ancestor.Runtime.Kind != "removed" && (ancestor.State != b.State || ancestor.Sequence < b.Sequence) || ancestor.Controller < b.Controller || a.Controller < ancestor.Controller {
		return 0, errors.New("applied runtime histories diverge or omit the committed predecessor")
	}
	return 1, nil
}

func (p *appliedOrigins) add(origin *appliedOrigin) error {
	p.Records += len(origin.Operations) + len(origin.Environments)
	if p.Records > maxAppliedOriginRecords {
		return errors.New("recovery applied-history evidence exceeds its aggregate limit")
	}
	p.Values = append(p.Values, origin)
	return nil
}

func (p appliedOrigins) origin(env uuid.UUID, tuple appliedTuple, parent uuid.UUID) (*appliedOrigin, error) {
	for _, origin := range p.Values {
		candidate, exists := origin.Environments[env]
		if exists && (parent == uuid.Nil || origin.Parent == parent) && candidate == tuple {
			return origin, nil
		}
	}
	return nil, errors.New("applied runtime has no authenticated source base")
}
