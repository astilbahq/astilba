package agent

import (
	"errors"
	"os"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func recoveryClose(c api.ExecutionCommand) bool {
	if c.Kind != "crosshost" {
		return false
	}
	body, err := crossHostCommand(c)
	return err == nil && body.Phase == "checkpoint-closed"
}

// Management emits this only when cancellation completed before reservation.
// A checkpoint alone never proves that an interrupted parent has finished.
func (j *Journal) executeRecoveryClose(c api.ExecutionCommand) error {
	body, err := crossHostCommand(c)
	if err != nil {
		return err
	}
	d, err := commandCheckpoint(c)
	if err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		if err := j.checkController(tx, c); err != nil {
			return err
		}
		var head recoveryHead
		err := j.get(tx, "recovery-heads", d.ParentID.String(), &head)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if head.Descriptor.ParentID != uuid.Nil && (!head.Primary || !deployment.SameRecoveryRoot(head.Descriptor, d) || c.ResourceId != head.UnitID || c.EnvironmentId != head.EnvironmentID) || c.Id != deployment.RecoveryCloseID(d.ParentID, c.ResourceId) || body.GroupHash != d.RootHash || body.Unit.Plan.Local == nil || !body.Unit.Plan.Local.Primary {
			return errors.New("checkpoint closure differs from the retained primary root")
		}
		active, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		if active.ParentID == d.ParentID {
			return errors.New("a reserved parent cannot be retired as checkpoint-only")
		}
		if err = tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
			var r Record
			if err := j.get(tx, "operations", string(k), &r); err != nil {
				return err
			}
			if r.Command.ParentId == nil || *r.Command.ParentId != d.ParentID {
				return nil
			}
			p, err := crossHostCommand(r.Command)
			metadataOpen := err == nil && p.Phase == "recovery-open" && commandGeneration(r.Command) > 0 && commandGeneration(r.Command) <= commandGeneration(c) && r.Command.Id == deployment.RecoveryOpenID(d.ParentID, c.ResourceId, commandGeneration(r.Command)) && (p.GroupHash == d.GroupHash || p.GroupHash == d.RootHash)
			if err != nil || p.Phase != "checkpoint" && p.Phase != "checkpoint-closed" && !metadataOpen || p.Unit.Plan.Hash != body.Unit.Plan.Hash {
				return errors.New("a parent with data-effect history requires normal completion or abort")
			}
			return nil
		}); err != nil {
			return err
		}
		var r Record
		if err = j.get(tx, "operations", c.Id.String(), &r); err != nil {
			return err
		}
		r.Report.State = "succeeded"
		r.Report.Note = "Management confirmed cancellation before reservation; only recovery metadata was retired."
		r.Report.Crosshost = crossHostReceipt(c, body, deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}})
		r.Report.Sequence++
		return j.put(tx, "operations", c.Id.String(), r)
	})
}
