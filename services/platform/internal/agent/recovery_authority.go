package agent

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type recoveredAuthority struct {
	Handover deployment.RecoveryHandover
	Open     bool
}

func recoverySentinelID(parent, server uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(parent, []byte("controller-recovery-hold:"+server.String()))
}

func (j *Journal) isRecoverySentinel(tx *bolt.Tx, id string) bool {
	var state recoveredAuthority
	if j.get(tx, "recovery-authority", "current", &state) != nil || state.Open {
		return false
	}
	return recoverySentinelID(state.Handover.Authority.ParentID, state.Handover.Network.ServerID).String() == id
}

func (j *Journal) recoveredAuthority() (recoveredAuthority, error) {
	var out recoveredAuthority
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "recovery-authority", "current", &out) })
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	return out, err
}

func (j *Journal) recoveryHeld() (bool, error) {
	a, err := j.recoveredAuthority()
	return a.Handover.Authority.ParentID != uuid.Nil && !a.Open, err
}

func recoveryOpen(c api.ExecutionCommand) bool {
	if c.Kind != "crosshost" {
		return false
	}
	body, err := crossHostCommand(c)
	return err == nil && body.Phase == "recovery-open"
}

// TransferRecovery replaces controller authority under the stopped agent's
// exclusive journal lock. It preserves effects and installs the replay hold in
// the same transaction; enrollment alone never releases it.
func (j *Journal) TransferRecovery(origin string, key []byte, token string, h deployment.RecoveryHandover) error {
	a := h.Authority
	if ValidateOrigin(origin) != nil || len(key) != ed25519.PublicKeySize || len(strings.TrimSpace(token)) != 43 || h.Descriptor.Validate() != nil || a.ParentID != h.Descriptor.ParentID || a.RootHash != h.Descriptor.RootHash || len(a.EvidenceHash) != 64 || len(h.UnitHash) != 64 || h.UnitID == uuid.Nil || h.EnvironmentID == uuid.Nil || h.Network.Validate() != nil || a.Controller <= a.PriorController || a.Controller < 1 {
		return errors.New("invalid exact-root recovery handover")
	}
	cfg, err := j.Config()
	if err != nil {
		return err
	}
	if h.Network.ServerID != cfg.ServerID {
		return errors.New("recovery network belongs to another host")
	}
	old, err := j.recoveredAuthority()
	if err != nil {
		return err
	}
	if old.Handover.Authority.ParentID != uuid.Nil && !old.Open && old.Handover.Authority.ParentID != a.ParentID {
		return errors.New("another controller recovery hold must be resolved first")
	}
	if cfg.ControllerGeneration == a.Controller && cfg.Origin == origin && bytes.Equal(cfg.PublicKey, key) && deployment.Hash(old.Handover) == deployment.Hash(h) {
		if cfg.Enrolled {
			return nil
		}
		cfg.EnrollmentToken = strings.TrimSpace(token)
		return j.SetConfig(cfg)
	}
	evidence, err := j.RecoveryEvidence()
	if err != nil {
		return err
	}
	if deployment.Hash(evidence) != a.EvidenceHash || cfg.ControllerGeneration != a.PriorController || cfg.ControllerGeneration == math.MaxInt64 {
		return errors.New("host evidence changed; repeat the guarded offline recovery")
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		state, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		if state.ParentID != uuid.Nil && (state.ParentID != a.ParentID || state.Unit.Plan.Hash != h.UnitHash) {
			return errors.New("another frozen reservation owns the host")
		}
		if err = tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
			var r Record
			if err := j.get(tx, "operations", string(k), &r); err != nil {
				return err
			}
			if j.isCrossHostSentinel(tx, string(k)) || j.isDistributedFence(tx, string(k)) || j.isRecoverySentinel(tx, string(k)) {
				return nil
			}
			if standaloneRestriction(r.Command) && !r.Acknowledged {
				r.Command.ControllerGeneration = &a.Controller
				return j.put(tx, "operations", string(k), r)
			}
			if r.Command.ParentId == nil || *r.Command.ParentId != a.ParentID {
				if !execution.Terminal(r.Report.State) {
					return errors.New("unrelated unfinished host operation requires its own recovery checkpoint")
				}
				r.Acknowledged = true
			} else {
				d, err := commandCheckpoint(r.Command)
				if err != nil || !deployment.SameRecoveryRoot(d, h.Descriptor) {
					return errors.New("recorded command differs from the recovered root")
				}
				if r.Command.Kind == "crosshost" {
					body, err := crossHostCommand(r.Command)
					if err != nil || body.Unit.Plan.Hash != h.UnitHash {
						return errors.New("recorded local plan differs from the recovered root")
					}
					priorOpen := body.Phase == "recovery-open" && r.Command.Id == deployment.RecoveryOpenID(a.ParentID, h.UnitID, commandGeneration(r.Command)) && commandGeneration(r.Command) <= a.PriorController
					unretained := body.Phase == "checkpoint" && h.Primary && deployment.UnretainedRecoveryDelivery(r.Command.Id, d, h.Descriptor)
					if unretained {
						var retained deployment.RecoveryDescriptor
						if err := j.get(tx, "recovery-checkpoints", d.CheckpointID.String(), &retained); !errors.Is(err, os.ErrNotExist) || r.Report.State == "succeeded" {
							return errors.New("a retained checkpoint cannot be retired as an interrupted delivery")
						}
					}
					if priorOpen || unretained {
						if !execution.Terminal(r.Report.State) {
							r.Report.State = "cancelled"
							r.Report.Note = "Superseded controller metadata; no data effect was inferred or replayed."
							r.Report.Sequence++
						}
						r.Acknowledged = true
						return j.put(tx, "operations", string(k), r)
					}
				}
				r.Command.ControllerGeneration = &a.Controller
			}
			return j.put(tx, "operations", string(k), r)
		}); err != nil {
			return err
		}
		if state.ParentID != uuid.Nil {
			state.Controller = a.Controller
			if err = j.put(tx, "cross-host", "active", state); err != nil {
				return err
			}
		}
		if err = tx.DeleteBucket([]byte("generations")); err != nil {
			return err
		}
		if _, err = tx.CreateBucket([]byte("generations")); err != nil {
			return err
		}
		cfg.Origin, cfg.PublicKey, cfg.ControllerGeneration = origin, append([]byte(nil), key...), a.Controller
		cfg.Enrolled, cfg.Credential, cfg.Pending, cfg.EnrollmentToken = false, "", "", strings.TrimSpace(token)
		if err = j.put(tx, "identity", "config", cfg); err != nil {
			return err
		}
		if err = j.put(tx, "recovery-authority", "current", recoveredAuthority{Handover: h}); err != nil {
			return err
		}
		sentinelID := recoverySentinelID(a.ParentID, cfg.ServerID)
		sentinel := Record{Command: api.ExecutionCommand{Id: sentinelID, ServerId: cfg.ServerID, EnvironmentId: h.EnvironmentID, ResourceId: h.UnitID, Kind: "visual", PlanHash: a.RootHash}, Report: api.AgentReport{Id: sentinelID, PlanHash: a.RootHash, Sequence: 1, State: "needs_attention", Steps: []api.ExecutionStep{}, Note: "Controller recovery requires an agent that understands the all-host replay hold."}, Checkpoint: "controller-recovery-hold"}
		if err = j.put(tx, "operations", sentinelID.String(), sentinel); err != nil {
			return err
		}
		var head recoveryHead
		err = j.get(tx, "recovery-heads", a.ParentID.String(), &head)
		if errors.Is(err, os.ErrNotExist) {
			return j.put(tx, "recovery-heads", a.ParentID.String(), recoveryHead{Descriptor: h.Descriptor, EnvironmentID: h.EnvironmentID, UnitID: h.UnitID, Primary: h.Primary})
		}
		if err != nil || !deployment.SameRecoveryRoot(head.Descriptor, h.Descriptor) {
			return errors.New("the retained recovery root differs")
		}
		return nil
	})
}

func (j *Journal) checkRecoveryOpen(tx *bolt.Tx, c api.ExecutionCommand) error {
	body, err := crossHostCommand(c)
	if err != nil {
		return err
	}
	d, err := commandCheckpoint(c)
	if err != nil {
		return err
	}
	var saved recoveredAuthority
	if err := j.get(tx, "recovery-authority", "current", &saved); err != nil {
		return err
	}
	h := saved.Handover
	if body.Phase != "recovery-open" || d.ParentID != h.Authority.ParentID || !deployment.SameRecoveryRoot(d, h.Descriptor) || body.Unit.Plan.Hash != h.UnitHash || c.ResourceId != h.UnitID || c.EnvironmentId != h.EnvironmentID || commandGeneration(c) != h.Authority.Controller || c.Id != deployment.RecoveryOpenID(h.Authority.ParentID, h.UnitID, h.Authority.Controller) {
		return errors.New("recovery-open differs from the explicitly transferred root")
	}
	return nil
}

func (j *Journal) executeRecoveryOpen(c api.ExecutionCommand) error {
	body, err := crossHostCommand(c)
	if err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		if err := j.checkController(tx, c); err != nil {
			return err
		}
		var saved recoveredAuthority
		if err := j.get(tx, "recovery-authority", "current", &saved); err != nil {
			return err
		}
		h := saved.Handover
		saved.Open = true
		if err := j.put(tx, "recovery-authority", "current", saved); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("operations")).Delete([]byte(recoverySentinelID(h.Authority.ParentID, h.Network.ServerID).String())); err != nil {
			return err
		}
		var r Record
		if err := j.get(tx, "operations", c.Id.String(), &r); err != nil {
			return err
		}
		r.Report.State, r.Report.Note = "succeeded", "The recovered controller confirmed every participant; the existing frozen parent may resume."
		r.Report.Crosshost = crossHostReceipt(c, body, deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}})
		r.Report.Sequence++
		return j.put(tx, "operations", c.Id.String(), r)
	})
}

func (j *Journal) recoveredRoot(c api.ExecutionCommand, p deployment.Plan) bool {
	s, err := j.recoveredAuthority()
	return err == nil && s.Open && c.ParentId != nil && *c.ParentId == s.Handover.Authority.ParentID && commandGeneration(c) == s.Handover.Authority.Controller && p.Hash == s.Handover.UnitHash
}

func recoveryAuthorityJSON(a deployment.RecoveryAuthority) *json.RawMessage {
	raw, _ := json.Marshal(a)
	message := json.RawMessage(raw)
	return &message
}

func (e *Engine) recoveredNetwork(c api.ExecutionCommand, m hostnetwork.Manifest) bool {
	s, err := e.Journal.recoveredAuthority()
	if err != nil || !s.Open || c.ParentId == nil || *c.ParentId != s.Handover.Authority.ParentID || commandGeneration(c) != s.Handover.Authority.Controller {
		return false
	}
	frozen := s.Handover.Network
	installed, err := e.networkHost().Snapshot(m.NetworkID)
	return err == nil && hostnetwork.Hash(frozen) == hostnetwork.Hash(m) && installed.Manifest.Generation <= m.Generation && installed.Manifest.ControllerGeneration <= m.ControllerGeneration
}
