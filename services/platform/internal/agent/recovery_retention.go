package agent

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func (j *Journal) completeRecoveryHead(tx *bolt.Tx, r Record) error {
	if r.Command.ParentId == nil || r.Command.Kind != "crosshost" || r.Report.State != "succeeded" {
		return nil
	}
	body, err := crossHostCommand(r.Command)
	if err != nil {
		return err
	}
	if body.Phase != "complete" && body.Phase != "abort" && body.Phase != "checkpoint-closed" {
		return nil
	}
	var head recoveryHead
	if err = j.get(tx, "recovery-heads", r.Command.ParentId.String(), &head); err != nil {
		return err
	}
	if head.Completed != 0 {
		return nil
	}
	head.Completed, err = tx.Bucket([]byte("recovery-heads")).NextSequence()
	if err != nil {
		return err
	}
	return j.put(tx, "recovery-heads", r.Command.ParentId.String(), head)
}

// Keep every unfinished chain and the two latest completed roots per
// environment. Durable tombstones bind later filesystem deletion to the exact
// owned capsule; a crash between the database commit and unlink is retryable.
func (j *Journal) pruneRecovery() error {
	j.recoveryMu.Lock()
	defer j.recoveryMu.Unlock()
	err := j.db.Update(func(tx *bolt.Tx) error {
		protected := map[uuid.UUID]bool{}
		active, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		protected[active.ParentID] = true
		var authority recoveredAuthority
		if err = j.get(tx, "recovery-authority", "current", &authority); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !authority.Open {
			protected[authority.Handover.Authority.ParentID] = true
		}
		if err = tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
			var r Record
			if err := j.get(tx, "operations", string(k), &r); err != nil {
				return err
			}
			if r.Command.ParentId != nil && (!r.Acknowledged || !execution.Terminal(r.Report.State)) {
				protected[*r.Command.ParentId] = true
			}
			return nil
		}); err != nil {
			return err
		}
		groups := map[uuid.UUID][]recoveryHead{}
		if err = tx.Bucket([]byte("recovery-heads")).ForEach(func(k, _ []byte) error {
			var h recoveryHead
			if err := j.get(tx, "recovery-heads", string(k), &h); err != nil {
				return err
			}
			if h.Completed != 0 {
				groups[h.EnvironmentID] = append(groups[h.EnvironmentID], h)
			}
			return nil
		}); err != nil {
			return err
		}
		remove := map[uuid.UUID]bool{}
		for _, heads := range groups {
			sort.Slice(heads, func(a, b int) bool { return heads[a].Completed > heads[b].Completed })
			for index, h := range heads {
				if index >= 2 && !protected[h.Descriptor.ParentID] {
					remove[h.Descriptor.ParentID] = true
				}
			}
		}
		var capsules []deployment.RecoveryDescriptor
		if err = tx.Bucket([]byte("recovery-checkpoints")).ForEach(func(k, _ []byte) error {
			var d deployment.RecoveryDescriptor
			if err := j.get(tx, "recovery-checkpoints", string(k), &d); err != nil {
				return err
			}
			if remove[d.ParentID] {
				capsules = append(capsules, d)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, d := range capsules {
			if err = j.put(tx, "recovery-gc", d.CheckpointID.String(), d); err != nil {
				return err
			}
			if err = tx.Bucket([]byte("recovery-checkpoints")).Delete([]byte(d.CheckpointID.String())); err != nil {
				return err
			}
		}
		var records [][]byte
		if err = tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
			var r Record
			if err := j.get(tx, "operations", string(k), &r); err != nil {
				return err
			}
			if r.Command.ParentId != nil && remove[*r.Command.ParentId] {
				records = append(records, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range records {
			if err = tx.Bucket([]byte("operations")).Delete(k); err != nil {
				return err
			}
		}
		for id := range remove {
			if err = tx.Bucket([]byte("recovery-heads")).Delete([]byte(id.String())); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var pending []deployment.RecoveryDescriptor
	if err = j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("recovery-gc")).ForEach(func(k, _ []byte) error {
			var d deployment.RecoveryDescriptor
			if err := j.get(tx, "recovery-gc", string(k), &d); err != nil {
				return err
			}
			pending = append(pending, d)
			return nil
		})
	}); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	dir := filepath.Join(j.dir, "recovery-checkpoints")
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("recovery capsule directory must be private and not a symlink")
	}
	for _, d := range pending {
		path := filepath.Join(dir, d.CheckpointID.String()+".capsule")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != int64(d.Bytes) || info.Mode().Perm()&0077 != 0 {
			return errors.New("retired recovery capsule requires local inspection")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if deployment.RecoveryDigest(body) != d.Digest {
			return errors.New("retired recovery capsule identity changed; nothing was deleted")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err = errors.Join(parent.Sync(), parent.Close()); err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		for _, d := range pending {
			if err := tx.Bucket([]byte("recovery-gc")).Delete([]byte(d.CheckpointID.String())); err != nil {
				return err
			}
		}
		return nil
	})
}
