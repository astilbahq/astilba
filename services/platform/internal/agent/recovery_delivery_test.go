package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// Losing management during a metadata fetch must not strand an unfetchable
// delivery ahead of every later command. Retiring it must leave replay held.
func TestRecoverySupersedesUnretainedDelivery(t *testing.T) {
	j, p := testVisualJournal(t)
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.MachineID = uuid.New()
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	member := uuid.New()
	identity, err := (hostnetwork.Host{Dir: j.dir}).Identity(member)
	if err != nil {
		t.Fatal(err)
	}
	network := hostnetwork.Manifest{Version: 1, NetworkID: uuid.New(), MembershipID: member, ServerID: p.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: identity.PublicKey, ListenPort: 51820}
	p.Local = &deployment.LocalPlacement{GroupID: uuid.New(), Primary: true, Network: network}
	p = p.WithHash()
	parent, id := uuid.New(), uuid.New()
	base := []byte("opaque-base")
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: p.Local.GroupID, GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(base), Bytes: len(base), Participants: []uuid.UUID{p.ServerID}}
	command := func(d deployment.RecoveryDescriptor, phase string, generation int64) api.ExecutionCommand {
		body := deployment.CrossHostCommand{ParentID: parent, GroupHash: d.GroupHash, Phase: phase, Unit: deployment.DistributedUnit{Plan: p}}
		raw, _ := json.Marshal(body)
		message := json.RawMessage(raw)
		descriptor, _ := json.Marshal(d)
		metadata := json.RawMessage(descriptor)
		return api.ExecutionCommand{Id: uuid.NewSHA1(d.CheckpointID, []byte("recovery-delivery")), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: generation, PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Crosshost: &message, RecoveryCheckpoint: &metadata}
	}
	engine := Engine{Journal: j, FetchCheckpoint: func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return base, nil }}
	deliver := func(c api.ExecutionCommand) error {
		if _, err := j.Accept(c); err != nil {
			return err
		}
		body, err := crossHostCommand(c)
		if err != nil {
			return err
		}
		return engine.executeRecoveryCheckpoint(context.Background(), c, body)
	}
	first := command(d, "checkpoint", 1)
	if err = deliver(first); err != nil {
		t.Fatal(err)
	}
	record, err := j.Record(first.Id)
	if err != nil || record.Report.State != "succeeded" {
		t.Fatal("base not retained", err)
	}
	if err = j.Acknowledge(first.Id, record.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	next := d
	next.Sequence, next.PreviousHash, next.CheckpointID = 1, d.Digest, uuid.New()
	segment := []byte("opaque-next")
	next.Digest, next.Bytes = deployment.RecoveryDigest(segment), len(segment)
	interrupted := command(next, "checkpoint", 2)
	engine.FetchCheckpoint = func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) {
		return nil, errors.New("controller disappeared")
	}
	if err = deliver(interrupted); err == nil {
		t.Fatal("failed transport hidden")
	}
	evidence, err := j.RecoveryEvidence()
	if err != nil {
		t.Fatal(err)
	}
	h := deployment.RecoveryHandover{Authority: deployment.RecoveryAuthority{ParentID: parent, RootHash: d.RootHash, EvidenceHash: deployment.Hash(evidence), Controller: 1}, Descriptor: d, UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, UnitHash: p.Hash, Primary: true, Network: network}
	if err = j.TransferRecovery("https://restored.example.test", make([]byte, 32), strings.Repeat("t", 43), h); err != nil {
		t.Fatal(err)
	}
	record, err = j.Record(interrupted.Id)
	if err != nil || record.Report.State != "cancelled" || !record.Acknowledged {
		t.Fatal("unretained metadata blocked replacement", err)
	}
	if held, err := j.recoveryHeld(); err != nil || !held {
		t.Fatal("metadata cleanup released replay", err)
	}
	open := command(d, "recovery-open", 100)
	open.Id = deployment.RecoveryOpenID(parent, p.UnitID, 1)
	controller := int64(1)
	open.ControllerGeneration = &controller
	if err = engine.Execute(context.Background(), open); err != nil {
		t.Fatal(err)
	}
	record, err = j.Record(open.Id)
	if err != nil || record.Report.State != "succeeded" {
		t.Fatal("open failed", err)
	}
	if err = j.Acknowledge(open.Id, record.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	next.CheckpointID = uuid.New()
	replacement := command(next, "checkpoint", 3)
	replacement.ControllerGeneration = &controller
	engine.FetchCheckpoint = func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return segment, nil }
	if err = deliver(replacement); err != nil {
		t.Fatal("replacement metadata was blocked", err)
	}
	record, err = j.Record(replacement.Id)
	if err != nil || record.Report.State != "succeeded" {
		t.Fatal("replacement was not retained", err)
	}
	if err = j.Acknowledge(replacement.Id, record.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	closed := command(next, "checkpoint-closed", 4)
	var closeBody deployment.CrossHostCommand
	if json.Unmarshal(*closed.Crosshost, &closeBody) != nil {
		t.Fatal("invalid closure payload")
	}
	closeBody.GroupHash = d.RootHash
	raw, _ := json.Marshal(closeBody)
	message := json.RawMessage(raw)
	closed.Crosshost = &message
	closed.Id, closed.PlanHash, closed.ControllerGeneration = deployment.RecoveryCloseID(parent, p.UnitID), closeBody.Digest(), &controller
	if err = engine.Execute(context.Background(), closed); err != nil {
		t.Fatal("no-effect recovery-open history blocked closure", err)
	}
	record, err = j.Record(closed.Id)
	if err != nil || record.Report.State != "succeeded" {
		t.Fatal("recovered checkpoint closure remained pending", err)
	}
}
