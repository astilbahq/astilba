package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func (j *Journal) RecoveryEvidence() (deployment.RecoveryEvidence, error) {
	out := deployment.RecoveryEvidence{Version: 1, Roots: []deployment.RecoveryRoot{}, Networks: []hostnetwork.Manifest{}}
	cfg, err := j.Config()
	if err != nil {
		return out, err
	}
	out.ServerID, out.MachineID, out.Controller = cfg.ServerID, cfg.MachineID, cfg.ControllerGeneration
	inv, err := j.Inventory()
	if err != nil {
		return out, err
	}
	out.Units, out.Schedules = inv.Units, inv.Schedules
	paths, err := filepath.Glob(filepath.Join(j.dir, "network", "*.json"))
	if err != nil {
		return out, err
	}
	for _, path := range paths {
		id, err := uuid.Parse(strings.TrimSuffix(filepath.Base(path), ".json"))
		if err != nil {
			return out, err
		}
		snapshot, err := (hostnetwork.Host{Dir: j.dir}).Snapshot(id)
		if err != nil {
			return out, err
		}
		if snapshot.Manifest.ServerID != cfg.ServerID {
			return out, errors.New("installed network belongs to another server")
		}
		out.Networks = append(out.Networks, snapshot.Manifest)
	}
	err = j.db.View(func(tx *bolt.Tx) error {
		active, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		out.ActiveParent = active.ParentID
		if err = tx.Bucket([]byte("recovery-heads")).ForEach(func(k, _ []byte) error {
			var head recoveryHead
			if err := j.get(tx, "recovery-heads", string(k), &head); err != nil {
				return err
			}
			root := deployment.RecoveryRoot{Descriptor: head.Descriptor, EnvironmentID: head.EnvironmentID, UnitID: head.UnitID, Primary: head.Primary}
			root.UnitHash, err = j.rootUnitHash(tx, head.Descriptor.ParentID)
			if err != nil {
				return err
			}
			if head.Descriptor.ParentID == active.ParentID {
				root.UnitHash = active.Unit.Plan.Hash
			}
			out.Roots = append(out.Roots, root)
			return nil
		}); err != nil {
			return err
		}
		return nil
	})
	sort.Slice(out.Roots, func(a, b int) bool {
		return out.Roots[a].Descriptor.ParentID.String() < out.Roots[b].Descriptor.ParentID.String()
	})
	if err == nil {
		out.ProofDigest, err = j.writeRecoveryProofs(io.Discard)
	}
	return out, err
}

// ExportRecovery requires the stopped agent's exclusive journal. The directory
// must be new: checkpoint files and public evidence form one private handoff.
func (j *Journal) ExportRecovery(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	evidence, err := j.RecoveryEvidence()
	if err != nil {
		return err
	}
	if err = j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("recovery-checkpoints")).ForEach(func(k, _ []byte) error {
			var d deployment.RecoveryDescriptor
			if err := j.get(tx, "recovery-checkpoints", string(k), &d); err != nil {
				return err
			}
			path := filepath.Join(j.dir, "recovery-checkpoints", d.CheckpointID.String()+".capsule")
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != int64(d.Bytes) {
				return errors.New("private recovery capsule is unavailable")
			}
			body, err := os.ReadFile(path)
			if err != nil || deployment.RecoveryDigest(body) != d.Digest {
				return errors.New("recovery capsule digest differs")
			}
			if err = writeRecoveryExportFile(filepath.Join(dir, d.CheckpointID.String()+".capsule"), body); err != nil {
				return err
			}
			raw, _ := json.Marshal(d)
			return writeRecoveryExportFile(filepath.Join(dir, d.CheckpointID.String()+".json"), raw)
		})
	}); err != nil {
		return err
	}
	proofFile, err := os.OpenFile(filepath.Join(dir, "proofs.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	digest, err := j.writeRecoveryProofs(proofFile)
	if err == nil {
		err = proofFile.Sync()
	}
	err = errors.Join(err, proofFile.Close())
	if err != nil {
		return err
	}
	if digest != evidence.ProofDigest {
		return errors.New("host proofs changed while exporting")
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "host.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}

func writeRecoveryExportFile(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(body)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func (j *Journal) rootUnitHash(tx *bolt.Tx, parent uuid.UUID) (string, error) {
	var hash string
	err := tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
		var r Record
		if err := j.get(tx, "operations", string(k), &r); err != nil {
			return err
		}
		if r.Command.ParentId == nil || *r.Command.ParentId != parent || r.Command.Kind != "crosshost" {
			return nil
		}
		body, err := crossHostCommand(r.Command)
		if err != nil {
			return err
		}
		if hash != "" && hash != body.Unit.Plan.Hash {
			return errors.New("recovery root has divergent local plans")
		}
		hash = body.Unit.Plan.Hash
		return nil
	})
	return hash, err
}

func (j *Journal) writeRecoveryProofs(out io.Writer) (string, error) {
	hash := sha256.New()
	encoder := json.NewEncoder(io.MultiWriter(out, hash))
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
			var r Record
			if err := j.get(tx, "operations", string(k), &r); err != nil {
				return err
			}
			if r.Command.ParentId == nil && !standaloneRestriction(r.Command) {
				return nil
			}
			header := r.Command
			header.Crosshost, header.Backup, header.Upgrade = nil, nil, nil
			frame, err := json.Marshal(header)
			if err != nil {
				return err
			}
			report := r.Report
			report.Note = "Retained host receipt."
			report.Steps = []api.ExecutionStep{}
			if report.Crosshost != nil {
				var receipt deployment.CrossHostReceipt
				if json.Unmarshal(*report.Crosshost, &receipt) != nil {
					return errors.New("invalid retained cross-host receipt")
				}
				for i := range receipt.Observation.Resources {
					receipt.Observation.Resources[i].Logs = ""
				}
				raw, _ := json.Marshal(receipt)
				message := json.RawMessage(raw)
				report.Crosshost = &message
			}
			raw, err := json.Marshal(report)
			if err != nil {
				return err
			}
			parent := uuid.Nil
			if r.Command.ParentId != nil {
				parent = *r.Command.ParentId
			}
			return encoder.Encode(deployment.RecoveryProof{ParentID: parent, ID: r.Command.Id, Header: frame, Checkpoint: r.Checkpoint, Report: raw})
		})
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}
