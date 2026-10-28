package agent

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type crossHostState struct {
	ParentID     uuid.UUID
	GroupHash    string
	Controller   int64
	Unit         deployment.DistributedUnit
	Phase        string
	DrainUntil   time.Time
	Hook         releaseState
	Publication  bool
	SentinelID   uuid.UUID
	Maintenance  *deployment.Maintenance
	Pause        *writerPause
	EffectAction string
	ResumeTarget *bool
	Restore      *restoreCutoverState
	Retiring     *retainedPool
}

func (j *Journal) crossHostIn(tx *bolt.Tx) (crossHostState, error) {
	var s crossHostState
	err := j.get(tx, "cross-host", "active", &s)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	return s, err
}
func (j *Journal) crossHost() (crossHostState, error) {
	var s crossHostState
	err := j.db.View(func(tx *bolt.Tx) error { var e error; s, e = j.crossHostIn(tx); return e })
	return s, err
}
func (j *Journal) hasCrossHostBarrier() (bool, error) {
	s, err := j.crossHost()
	return s.ParentID != uuid.Nil, err
}
func (j *Journal) isCrossHostSentinel(tx *bolt.Tx, id string) bool {
	s, err := j.crossHostIn(tx)
	return err == nil && s.ParentID != uuid.Nil && id == s.SentinelID.String()
}
func (j *Journal) crossHostAdmission(tx *bolt.Tx, c api.ExecutionCommand) error {
	s, err := j.crossHostIn(tx)
	if err != nil {
		return err
	}
	if s.ParentID == uuid.Nil {
		return nil
	}
	if c.Kind == "network" && c.Network != nil {
		var n hostnetwork.Command
		if hostnetwork.Decode(*c.Network, &n) == nil && n.Action == "restrict" {
			return nil
		}
	}
	if c.ParentId == nil || *c.ParentId != s.ParentID {
		return errors.New("a coordinated host reservation requires reconciliation")
	}
	if c.Kind == "crosshost" && c.Crosshost != nil {
		var command deployment.CrossHostCommand
		if hostnetwork.Decode(*c.Crosshost, &command) == nil && command.ParentID == s.ParentID && command.GroupHash == s.GroupHash && command.Unit.Plan.Hash == s.Unit.Plan.Hash && deployment.Hash(command.Maintenance) == deployment.Hash(s.Maintenance) {
			return nil
		}
	}
	if s.Maintenance != nil && s.Unit.Plan.Local.Primary && s.Phase == "effect" && maintenanceLeafMatches(c, s) {
		return nil
	}
	if c.Kind == "network" && c.Network != nil {
		var n hostnetwork.Command
		if hostnetwork.Decode(*c.Network, &n) == nil && n.Manifest != nil && n.Manifest.NetworkID == s.Unit.Plan.Local.Network.NetworkID && hostnetwork.Hash(*n.Manifest) == hostnetwork.Hash(s.Unit.Plan.Local.Network) {
			return nil
		}
	}
	return errors.New("command does not match the frozen host reservation")
}
func (j *Journal) reserveCrossHost(c api.ExecutionCommand, command deployment.CrossHostCommand) (crossHostState, error) {
	var out crossHostState
	err := j.db.Update(func(tx *bolt.Tx) error {
		s, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		if s.ParentID != uuid.Nil {
			if s.ParentID != command.ParentID || s.GroupHash != command.GroupHash || s.Unit.Plan.Hash != command.Unit.Plan.Hash {
				return errors.New("another coordinated operation owns this host")
			}
			out = s
			return nil
		}
		var cfg Config
		if err = j.get(tx, "identity", "config", &cfg); err != nil {
			return err
		}
		s = crossHostState{ParentID: command.ParentID, GroupHash: command.GroupHash, Controller: cfg.ControllerGeneration, Unit: command.Unit, Maintenance: command.Maintenance, Phase: "reserved", SentinelID: uuid.NewSHA1(command.ParentID, []byte("cross-host-reservation:"+cfg.ServerID.String()))}
		sentinel := Record{Command: api.ExecutionCommand{Id: s.SentinelID, ServerId: cfg.ServerID, EnvironmentId: c.EnvironmentId, ResourceId: c.ResourceId, Kind: "visual", Generation: c.Generation, PlanHash: command.GroupHash}, Report: api.AgentReport{Id: s.SentinelID, Generation: c.Generation, PlanHash: command.GroupHash, Sequence: 1, State: "needs_attention", Steps: []api.ExecutionStep{}, Note: "A coordinated cross-host reservation requires a compatible agent and explicit reconciliation."}, Checkpoint: "cross-host-reservation"}
		if err = j.put(tx, "operations", s.SentinelID.String(), sentinel); err != nil {
			return err
		}
		if err = j.put(tx, "cross-host", "active", s); err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}
func (j *Journal) saveCrossHost(s crossHostState) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		old, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		if old.ParentID != s.ParentID || old.GroupHash != s.GroupHash || old.Unit.Plan.Hash != s.Unit.Plan.Hash {
			return errors.New("coordinated reservation identity changed")
		}
		return j.put(tx, "cross-host", "active", s)
	})
}
func crossHostCommand(c api.ExecutionCommand) (deployment.CrossHostCommand, error) {
	var command deployment.CrossHostCommand
	if c.Crosshost == nil || hostnetwork.Decode(*c.Crosshost, &command) != nil || command.Digest() != c.PlanHash || c.ParentId == nil || *c.ParentId != command.ParentID || command.ParentID == uuid.Nil || len(command.GroupHash) != 64 || command.Unit.Plan.ServerID != c.ServerId || command.Unit.Plan.EnvironmentID != c.EnvironmentId || command.Unit.Plan.UnitID != c.ResourceId {
		return command, errors.New("invalid coordinated command identity")
	}
	return command, nil
}
func crossHostReceipt(c api.ExecutionCommand, command deployment.CrossHostCommand, observation deployment.Observation) *json.RawMessage {
	raw, _ := json.Marshal(deployment.CrossHostReceipt{ParentID: command.ParentID, GroupHash: command.GroupHash, UnitID: command.Unit.Plan.UnitID, Phase: command.Phase, PlanHash: command.Unit.Plan.Hash, Observation: observation})
	v := json.RawMessage(raw)
	return &v
}

func (e *Engine) finishCrossHost(c api.ExecutionCommand, command deployment.CrossHostCommand, o deployment.Observation, note string, release bool) error {
	raw := crossHostReceipt(c, command, o)
	if len(*raw) > deployment.ObservationLimit+2048 {
		return e.finish(c.Id, "needs_attention", "The coordinated observation exceeds its reporting bound.")
	}
	return e.Journal.db.Update(func(tx *bolt.Tx) error {
		var r Record
		if err := e.Journal.get(tx, "operations", c.Id.String(), &r); err != nil {
			return err
		}
		r.Report.Crosshost = raw
		r.Report.State = "succeeded"
		r.Report.Note = note
		r.Report.Sequence++
		if err := e.Journal.put(tx, "operations", c.Id.String(), r); err != nil {
			return err
		}
		if !release {
			return nil
		}
		s, err := e.Journal.crossHostIn(tx)
		if err != nil {
			return err
		}
		if s.ParentID != command.ParentID || s.GroupHash != command.GroupHash {
			return errors.New("reservation changed before completion")
		}
		if command.Phase == "complete" && !command.Unit.Plan.Local.Primary && len(command.Unit.Plan.LocalApplications()) == 0 {
			if err = tx.Bucket([]byte("units")).Delete([]byte(command.Unit.Plan.UnitID.String())); err != nil {
				return err
			}
		}
		if err = tx.Bucket([]byte("operations")).Delete([]byte(s.SentinelID.String())); err != nil {
			return err
		}
		return tx.Bucket([]byte("cross-host")).Delete([]byte("active"))
	})
}

// RequireUnreservedHost also fences local recovery and update CLIs that do not
// accept their effects through the ordinary command journal.
func (j *Journal) RequireUnreservedHost() error {
	if held, err := j.recoveryHeld(); err != nil {
		return err
	} else if held {
		return errors.New("the recovered controller has not opened its all-host authority barrier")
	}
	blocked, err := j.hasCrossHostBarrier()
	if err != nil {
		return err
	}
	if blocked {
		return errors.New("resolve the coordinated host reservation before this local host effect")
	}
	return nil
}
