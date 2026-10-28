package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type recoveryHead struct {
	Descriptor    deployment.RecoveryDescriptor
	EnvironmentID uuid.UUID
	UnitID        uuid.UUID
	Primary       bool
	Completed     uint64
}

func commandCheckpoint(c api.ExecutionCommand) (deployment.RecoveryDescriptor, error) {
	var d deployment.RecoveryDescriptor
	if c.RecoveryCheckpoint == nil || hostnetwork.Decode(*c.RecoveryCheckpoint, &d) != nil || d.Validate() != nil || c.ParentId == nil || *c.ParentId != d.ParentID || !slices.Contains(d.Participants, c.ServerId) {
		return d, errors.New("the command has no valid frozen recovery checkpoint")
	}
	return d, nil
}

func (j *Journal) recoveryHead(parent uuid.UUID) (recoveryHead, error) {
	var head recoveryHead
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "recovery-heads", parent.String(), &head) })
	return head, err
}

func (j *Journal) retainCheckpoint(c api.ExecutionCommand, d deployment.RecoveryDescriptor, body []byte) error {
	j.recoveryMu.Lock()
	defer j.recoveryMu.Unlock()
	if d.Validate() != nil || len(body) != d.Bytes || deployment.RecoveryDigest(body) != d.Digest {
		return errors.New("recovery envelope differs from its assigned descriptor")
	}
	head, err := j.recoveryHead(d.ParentID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if head.Descriptor.ParentID != uuid.Nil {
		old := head.Descriptor
		if !deployment.SameRecoveryRoot(old, d) || head.EnvironmentID != c.EnvironmentId || head.UnitID != c.ResourceId {
			return errors.New("the frozen recovery root changed")
		}
		if d.Sequence > old.Sequence && (d.Sequence != old.Sequence+1 || d.PreviousHash != old.Digest) {
			return errors.New("recovery continuation chain is incomplete")
		}
	} else if d.Sequence != 0 {
		return errors.New("the base recovery checkpoint must be retained first")
	}
	dir := filepath.Join(j.dir, "recovery-checkpoints")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("recovery checkpoint directory must be private and not a symlink")
	}
	path := filepath.Join(dir, d.CheckpointID.String()+".capsule")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != int64(d.Bytes) {
			return errors.New("existing recovery capsule requires local inspection")
		}
		stored, err := os.ReadFile(path)
		if err != nil || deployment.RecoveryDigest(stored) != d.Digest {
			return errors.New("existing recovery capsule differs from the frozen descriptor")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		file, err := os.CreateTemp(dir, ".checkpoint-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if err = file.Chmod(0600); err == nil {
			_, err = file.Write(body)
		}
		if err == nil {
			err = file.Sync()
		}
		err = errors.Join(err, file.Close())
		if err == nil {
			err = os.Rename(file.Name(), path)
		}
		if err != nil {
			return err
		}
		parent, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = errors.Join(parent.Sync(), parent.Close())
		if err != nil {
			return err
		}
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		var existing deployment.RecoveryDescriptor
		err := j.get(tx, "recovery-checkpoints", d.CheckpointID.String(), &existing)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && deployment.Hash(existing) != deployment.Hash(d) {
			return errors.New("recovery checkpoint identity changed")
		}
		if err := j.put(tx, "recovery-checkpoints", d.CheckpointID.String(), d); err != nil {
			return err
		}
		if head.Descriptor.ParentID == uuid.Nil || d.Sequence >= head.Descriptor.Sequence {
			return j.put(tx, "recovery-heads", d.ParentID.String(), recoveryHead{Descriptor: d, EnvironmentID: c.EnvironmentId, UnitID: c.ResourceId, Primary: true, Completed: head.Completed})
		}
		return nil
	})
}

// Public descriptors on peers bind the same root without exposing the primary's
// encrypted metadata. The primary must already have retained the exact capsule.
func (j *Journal) requireRecoveryCheckpoint(c api.ExecutionCommand, unitID uuid.UUID, primary bool) error {
	d, err := commandCheckpoint(c)
	if err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		var head recoveryHead
		err := j.get(tx, "recovery-heads", d.ParentID.String(), &head)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (!deployment.SameRecoveryRoot(head.Descriptor, d) || head.EnvironmentID != c.EnvironmentId || head.UnitID != unitID) {
			return errors.New("the recovery descriptor differs from this host's frozen root")
		}
		if primary {
			var retained deployment.RecoveryDescriptor
			if err := j.get(tx, "recovery-checkpoints", d.CheckpointID.String(), &retained); err != nil || deployment.Hash(retained) != deployment.Hash(d) {
				return errors.New("the assigned primary has not retained this recovery checkpoint")
			}
			return nil
		}
		if err != nil || d.Sequence > head.Descriptor.Sequence {
			return j.put(tx, "recovery-heads", d.ParentID.String(), recoveryHead{Descriptor: d, EnvironmentID: c.EnvironmentId, UnitID: unitID, Completed: head.Completed})
		}
		return nil
	})
}

func (e *Engine) executeRecoveryCheckpoint(ctx context.Context, c api.ExecutionCommand, command deployment.CrossHostCommand) error {
	d, err := commandCheckpoint(c)
	if err != nil || !command.Unit.Plan.Local.Primary || d.GroupID != command.Unit.Plan.Local.GroupID || command.Maintenance == nil && d.GroupHash != command.GroupHash || command.Maintenance != nil && d.RootHash != command.GroupHash {
		return e.finish(c.Id, "failed", "Recovery metadata does not match this frozen primary root.")
	}
	// Later segments are also needed to describe cancellation cleanup. Retaining
	// metadata never grants permission to execute the cancelled phase.
	if e.cancelled(c.Id) && d.Sequence == 0 {
		return e.finish(c.Id, "cancelled", "Cancelled before recovery metadata or host effects were admitted.")
	}
	if e.FetchCheckpoint == nil {
		return errors.New("recovery metadata transport is unavailable; execution remains held")
	}
	body, err := e.FetchCheckpoint(ctx, d)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return e.retryRecoveryDelivery(c.Id, "Recovery metadata could not be downloaded; delivery will retry before this phase can execute.")
	}
	if err := e.Journal.retainCheckpoint(c, d, body); err != nil {
		return e.retryRecoveryDelivery(c.Id, "Recovery metadata could not be durably retained; repair local storage while execution remains held.")
	}
	e.point("recovery-checkpoint-retained")
	receipt := deployment.CrossHostReceipt{ParentID: command.ParentID, GroupHash: command.GroupHash, UnitID: c.ResourceId, PlanHash: command.Unit.Plan.Hash, Phase: command.Phase, CheckpointHash: d.Digest}
	receipt.Observation.Resources = []deployment.Resource{}
	receipt.Observation.ObservedAt = time.Now().UTC()
	raw, _ := json.Marshal(receipt)
	return e.Journal.Mutate(c.Id, func(r *Record) {
		message := json.RawMessage(raw)
		r.Report.Crosshost = &message
		r.Report.State = "succeeded"
		r.Report.Note = "The assigned primary durably retained recovery metadata before any host reservation."
		r.Report.Sequence++
	})
}

func (e *Engine) retryRecoveryDelivery(id uuid.UUID, note string) error {
	err := e.Journal.db.Update(func(tx *bolt.Tx) error {
		var r Record
		if err := e.Journal.get(tx, "operations", id.String(), &r); err != nil {
			return err
		}
		if r.Report.Note == note {
			return nil
		}
		r.Report.Note = note
		r.Report.Sequence++
		return e.Journal.put(tx, "operations", id.String(), r)
	})
	return errors.Join(err, errors.New(note))
}

func (c *Client) downloadCheckpoint(ctx context.Context, d deployment.RecoveryDescriptor) ([]byte, error) {
	cfg, err := c.Journal.Config()
	if err != nil || !cfg.Enrolled || ValidateOrigin(cfg.Origin) != nil {
		return nil, errors.New("enrolled controller authority is required for checkpoint delivery")
	}
	path := "/agent/v1/operations/" + d.ParentID.String() + "/checkpoints/" + d.CheckpointID.String()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Origin+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.ServerID.String()+"."+cfg.Credential)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > int64(d.Bytes) {
		return nil, errors.New("checkpoint delivery was rejected or oversized")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(d.Bytes)+1))
	if err != nil || len(body) != d.Bytes || deployment.RecoveryDigest(body) != d.Digest {
		return nil, errors.New("checkpoint delivery differs from its frozen digest")
	}
	return body, nil
}
