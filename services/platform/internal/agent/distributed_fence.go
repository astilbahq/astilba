package agent

import (
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"os"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type distributedFence struct {
	UnitID uuid.UUID `json:"unitId"`
}

func (j *Journal) isDistributedFence(tx *bolt.Tx, id string) bool {
	var fence distributedFence
	return j.get(tx, "distributed-fences", id, &fence) == nil && fence.UnitID != uuid.Nil
}

// Older agents must keep refusing local-only maintenance after a distributed
// rollout completes. Current agents recognize this mirror as a compatibility
// fence rather than an execution report awaiting controller acknowledgement.
func (j *Journal) syncDistributedFence(tx *bolt.Tx, p deployment.Plan) error {
	id := uuid.NewSHA1(p.UnitID, []byte("distributed-runtime-fence:v1"))
	if !p.Specification.HasRemotePlacement() || p.Local != nil && !p.Local.Primary && len(p.LocalApplications()) == 0 {
		if err := tx.Bucket([]byte("distributed-fences")).Delete([]byte(id.String())); err != nil {
			return err
		}
		var record Record
		err := j.get(tx, "operations", id.String(), &record)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && record.Checkpoint == "distributed-runtime-fence" {
			return tx.Bucket([]byte("operations")).Delete([]byte(id.String()))
		}
		return nil
	}
	record := Record{Command: api.ExecutionCommand{Id: id, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "visual", PlanHash: p.Hash}, Report: api.AgentReport{Id: id, PlanHash: p.Hash, Sequence: 1, State: "needs_attention", Steps: []api.ExecutionStep{}, Note: "Distributed runtime requires a compatible agent for deployment and coordinated maintenance."}, Checkpoint: "distributed-runtime-fence"}
	if err := j.put(tx, "distributed-fences", id.String(), distributedFence{UnitID: p.UnitID}); err != nil {
		return err
	}
	return j.put(tx, "operations", id.String(), record)
}

func (e *Engine) distributedRuntimeAdmission(c api.ExecutionCommand) error {
	var unit uuid.UUID
	switch c.Kind {
	case "visual":
		var p deployment.Payload
		if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil {
			return nil
		}
		unit = p.Plan.UnitID
		active, err := e.Journal.unit(unit)
		if err != nil {
			return err
		}
		if !active.Plan.Specification.HasRemotePlacement() {
			return nil
		}
		if (p.Action == "observe" || p.Action == "delete-volume" && p.Plan.Local != nil && p.Plan.Local.Primary) && p.Plan.Hash == active.Plan.Hash {
			return nil
		}
		return errors.New("distributed runtime requires a coordinated operation; a local-only visual command is refused")
	case "upgrade":
		var command upgrade.Command
		if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &command) != nil {
			return nil
		}
		unit = command.Plan.Source.UnitID
	case "backup", "schedule":
		var command backup.Command
		if c.Backup == nil || json.Unmarshal(*c.Backup, &command) != nil {
			return nil
		}
		unit = command.Installation.Policy.Plan.UnitID
		active, err := e.Journal.unit(unit)
		if err != nil {
			return err
		}
		if !active.Plan.Specification.HasRemotePlacement() {
			return nil
		}
		if command.Installation.Policy.Plan.Hash != active.Plan.Hash {
			return errors.New("the distributed policy requires renewed approval for the current placement")
		}
		if command.Action != "backup" || !command.Installation.Policy.RequiresQuiescence() {
			return nil
		}
	default:
		return nil
	}
	active, err := e.Journal.unit(unit)
	if err != nil {
		return err
	}
	if active.Plan.Specification.HasRemotePlacement() && c.ParentId == nil {
		return errors.New("distributed writer maintenance requires a coordinated operation")
	}
	return nil
}
