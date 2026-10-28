package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

// Capsules may exceed the encrypted journal record limit. The private file and
// its journal identity must survive restart together before a host can ACK.
func TestRecoveryCapsuleDurabilityAndPrivateStorage(t *testing.T) {
	j, p := testVisualJournal(t)
	dir := j.dir
	body := bytes.Repeat([]byte("opaque-management-ciphertext"), 23000)
	id, parent := uuid.New(), uuid.New()
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: uuid.New(), GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(body), Bytes: len(body), Participants: []uuid.UUID{p.ServerID}}
	raw, _ := json.Marshal(d)
	message := json.RawMessage(raw)
	c := api.ExecutionCommand{ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, RecoveryCheckpoint: &message}
	if err := j.retainCheckpoint(c, d, body); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	head, err := j.recoveryHead(parent)
	if err != nil || deployment.Hash(head.Descriptor) != deployment.Hash(d) || head.UnitID != c.ResourceId {
		t.Fatal("restart lost the acknowledged checkpoint identity", err)
	}
	path := filepath.Join(dir, "recovery-checkpoints", id.String()+".capsule")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("capsule was not private", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, body) {
		t.Fatal("restart lost the opaque capsule", err)
	}
	if err = j.retainCheckpoint(c, d, body); err != nil {
		t.Fatal("exact replay was not idempotent", err)
	}
	if err = os.WriteFile(path, bytes.Repeat([]byte("x"), len(body)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = j.retainCheckpoint(c, d, body); err == nil {
		t.Fatal("tampered storage was acknowledged")
	}
}

func TestRecoveryContinuationRetriesMetadataWithoutRunningCancelledEffects(t *testing.T) {
	j, p := testVisualJournal(t)
	parent, id := uuid.New(), uuid.New()
	p.Local = &deployment.LocalPlacement{GroupID: uuid.New(), Primary: true}
	p = p.WithHash()
	base := []byte("base-envelope")
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: p.Local.GroupID, GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(base), Bytes: len(base), Participants: []uuid.UUID{p.ServerID}}
	body := deployment.CrossHostCommand{ParentID: parent, GroupHash: d.GroupHash, Phase: "checkpoint", Unit: deployment.DistributedUnit{Plan: p}}
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: 1, PlanHash: body.Digest(), Crosshost: &message}
	if err := j.retainCheckpoint(c, d, base); err != nil {
		t.Fatal(err)
	}
	continuation := []byte("phase-envelope")
	d.Sequence, d.PreviousHash, d.CheckpointID, d.Bytes = 1, d.Digest, uuid.New(), len(continuation)
	d.Digest = deployment.RecoveryDigest(continuation)
	descriptor, _ := json.Marshal(d)
	metadata := json.RawMessage(descriptor)
	c.RecoveryCheckpoint = &metadata
	c.CancelRequested = true
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Journal: j, FetchCheckpoint: func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) {
		return nil, errors.New("temporary transport failure")
	}}
	if err := engine.executeRecoveryCheckpoint(context.Background(), c, body); err == nil {
		t.Fatal("transport failure was hidden")
	}
	record, err := j.Record(c.Id)
	if err != nil || record.Report.State != "running" {
		t.Fatal("temporary metadata failure became an unrecoverable terminal outcome", err)
	}
	engine.FetchCheckpoint = func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return continuation, nil }
	if err := engine.executeRecoveryCheckpoint(context.Background(), c, body); err != nil {
		t.Fatal(err)
	}
	record, err = j.Record(c.Id)
	if err != nil || record.Report.State != "succeeded" || record.Report.Crosshost == nil {
		t.Fatal("same delivery could not recover after transport repair", err)
	}
	if barrier, err := j.hasCrossHostBarrier(); err != nil || barrier {
		t.Fatal("metadata retry created a host resource reservation", err)
	}
}

func TestRecoveryCapsuleRefusesSymlinkStorage(t *testing.T) {
	j, p := testVisualJournal(t)
	defer j.Close()
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(j.dir, "recovery-checkpoints")); err != nil {
		t.Fatal(err)
	}
	body := []byte("opaque")
	id, parent := uuid.New(), uuid.New()
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: uuid.New(), GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(body), Bytes: len(body), Participants: []uuid.UUID{p.ServerID}}
	c := api.ExecutionCommand{ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID}
	if err := j.retainCheckpoint(c, d, body); err == nil {
		t.Fatal("symlink storage was acknowledged")
	}
	files, err := os.ReadDir(out)
	if err != nil || len(files) != 0 {
		t.Fatal("checkpoint escaped its private directory", err)
	}
	if _, err = j.recoveryHead(parent); !os.IsNotExist(err) {
		t.Fatal("rejected capsule changed the durable head", err)
	}
}
