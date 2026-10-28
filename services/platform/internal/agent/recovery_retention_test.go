package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func TestRecoveryRetentionPreservesUnfinishedChains(t *testing.T) {
	j, p := testVisualJournal(t)
	root := func(completed bool, generation int64) (deployment.RecoveryDescriptor, api.ExecutionCommand) {
		parent, id := uuid.New(), uuid.New()
		body := []byte("opaque-" + parent.String())
		d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: uuid.New(), GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(body), Bytes: len(body), Participants: []uuid.UUID{p.ServerID}}
		command := deployment.CrossHostCommand{ParentID: parent, GroupHash: d.GroupHash, Phase: "complete", Unit: deployment.DistributedUnit{Plan: p}}
		raw, _ := json.Marshal(command)
		message := json.RawMessage(raw)
		c := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: generation, PlanHash: command.Digest(), Crosshost: &message}
		if err := j.retainCheckpoint(c, d, body); err != nil {
			t.Fatal(err)
		}
		if completed {
			if _, err := j.Accept(c); err != nil {
				t.Fatal(err)
			}
			if err := j.Mutate(c.Id, func(r *Record) { r.Report.State = "succeeded" }); err != nil {
				t.Fatal(err)
			}
			r, err := j.Record(c.Id)
			if err != nil {
				t.Fatal(err)
			}
			if err = j.Acknowledge(c.Id, r.Report.Sequence); err != nil {
				t.Fatal(err)
			}
		}
		return d, c
	}
	unfinished, _ := root(false, 1)
	first, firstCommand := root(true, 2)
	second, _ := root(true, 3)
	third, _ := root(true, 4)
	for _, d := range []deployment.RecoveryDescriptor{unfinished, second, third} {
		if _, err := j.recoveryHead(d.ParentID); err != nil {
			t.Fatal("retention removed current, previous or unfinished recovery", err)
		}
		if _, err := os.Stat(filepath.Join(j.dir, "recovery-checkpoints", d.CheckpointID.String()+".capsule")); err != nil {
			t.Fatal("retention removed a required capsule", err)
		}
	}
	if _, err := j.recoveryHead(first.ParentID); !os.IsNotExist(err) {
		t.Fatal("third completed history was not retired", err)
	}
	if _, err := j.Record(firstCommand.Id); !os.IsNotExist(err) {
		t.Fatal("retired history remained permanently pinned in operation journal", err)
	}
	if _, err := os.Stat(filepath.Join(j.dir, "recovery-checkpoints", first.CheckpointID.String()+".capsule")); !os.IsNotExist(err) {
		t.Fatal("retired capsule file was not removed", err)
	}
	dir := j.dir
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err = j.recoveryHead(unfinished.ParentID); err != nil {
		t.Fatal("restart lost unfinished recovery", err)
	}
}

func TestRecoveryDeletionResumesWithoutRemovingChangedFiles(t *testing.T) {
	j, p := testVisualJournal(t)
	body := []byte("opaque-gc-owned")
	id, parent := uuid.New(), uuid.New()
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: uuid.New(), GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(body), Bytes: len(body), Participants: []uuid.UUID{p.ServerID}}
	c := api.ExecutionCommand{ParentId: &parent, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID}
	if err := j.retainCheckpoint(c, d, body); err != nil {
		t.Fatal(err)
	}
	// Stop after the durable database half of collection, before file unlink.
	if err := j.db.Update(func(tx *bolt.Tx) error {
		if err := j.put(tx, "recovery-gc", id.String(), d); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("recovery-checkpoints")).Delete([]byte(id.String())); err != nil {
			return err
		}
		return tx.Bucket([]byte("recovery-heads")).Delete([]byte(parent.String()))
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(j.dir, "recovery-checkpoints", id.String()+".capsule")
	directory := filepath.Dir(path)
	owned := directory + ".owned"
	backup := t.TempDir()
	if err := os.Chmod(backup, 0700); err != nil {
		t.Fatal(err)
	}
	backupFile := filepath.Join(backup, filepath.Base(path))
	if err := os.WriteFile(backupFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, owned); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, directory); err != nil {
		t.Fatal(err)
	}
	if err := j.pruneRecovery(); err == nil {
		t.Fatal("collection followed a substituted directory into a backup")
	}
	if got, err := os.ReadFile(backupFile); err != nil || string(got) != string(body) {
		t.Fatal("collection removed a backup copy outside the owned directory", err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(owned, directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.pruneRecovery(); err == nil {
		t.Fatal("changed file was deleted without matching its recorded ownership")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "changed-content" {
		t.Fatal("unrecognized file was altered", err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	dir := j.dir
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("restart did not finish owned capsule deletion", err)
	}
}

func TestRecoveryClosureBoundsCheckpointOnlyCancellations(t *testing.T) {
	j, p := testVisualJournal(t)
	engine := Engine{Journal: j}
	var first deployment.RecoveryDescriptor
	for index := 0; index < 4; index++ {
		parent, id := uuid.New(), uuid.New()
		body := []byte("opaque-" + parent.String())
		d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: uuid.New(), GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(body), Bytes: len(body), Participants: []uuid.UUID{p.ServerID}}
		if index == 0 {
			first = d
		}
		plan := p
		plan.Local = &deployment.LocalPlacement{GroupID: d.GroupID, Primary: true}
		plan = plan.WithHash()
		payload := deployment.CrossHostCommand{ParentID: parent, GroupHash: d.GroupHash, Phase: "checkpoint", Unit: deployment.DistributedUnit{Plan: plan}}
		raw, _ := json.Marshal(payload)
		message := json.RawMessage(raw)
		metadata, _ := json.Marshal(d)
		descriptor := json.RawMessage(metadata)
		c := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: int64(index*2 + 1), PlanHash: payload.Digest(), Deadline: time.Now().Add(time.Hour), Crosshost: &message, RecoveryCheckpoint: &descriptor}
		if _, err := j.Accept(c); err != nil {
			t.Fatal(err)
		}
		engine.FetchCheckpoint = func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return body, nil }
		if err := engine.executeRecoveryCheckpoint(context.Background(), c, payload); err != nil {
			t.Fatal(err)
		}
		r, err := j.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		if err = j.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		head, err := j.recoveryHead(parent)
		if err != nil || head.Completed != 0 {
			t.Fatal("metadata acknowledgement inferred parent completion", err)
		}
		payload.Phase, payload.GroupHash = "checkpoint-closed", d.RootHash
		raw, _ = json.Marshal(payload)
		message = json.RawMessage(raw)
		c.Id, c.Generation, c.PlanHash = deployment.RecoveryCloseID(parent, p.UnitID), int64(index*2+2), payload.Digest()
		c.Crosshost = &message
		if err = engine.Execute(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		r, err = j.Record(c.Id)
		if err != nil || r.Report.State != "succeeded" {
			t.Fatal("authoritative no-effect closure was not accepted", err)
		}
		if err = j.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err := j.RecoveryEvidence()
	if err != nil || len(evidence.Roots) != 2 {
		t.Fatal("cancelled roots grew beyond finished-history retention", len(evidence.Roots), err)
	}
	if _, err = j.recoveryHead(first.ParentID); !os.IsNotExist(err) {
		t.Fatal("old checkpoint-only cancellation remained pinned", err)
	}
}
