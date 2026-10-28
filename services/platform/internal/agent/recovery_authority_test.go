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
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

func TestRecoveryTransferHoldsReplayAndPreservesFrozenReservation(t *testing.T) {
	j, p := testVisualJournal(t)
	cfg, _ := j.Config()
	cfg.MachineID = uuid.New()
	if err := j.SetConfig(cfg); err != nil {
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
	envelope := []byte("opaque-management-envelope")
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: p.Local.GroupID, GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(envelope), Bytes: len(envelope), Participants: []uuid.UUID{p.ServerID}}
	makeCommand := func(phase string, generation int64) api.ExecutionCommand {
		body := deployment.CrossHostCommand{ParentID: parent, GroupHash: d.GroupHash, Phase: phase, Unit: deployment.DistributedUnit{Plan: p}, Secrets: map[string]string{"canary": "private-host-secret-canary"}}
		raw, _ := json.Marshal(body)
		message := json.RawMessage(raw)
		descriptor, _ := json.Marshal(d)
		metadata := json.RawMessage(descriptor)
		return api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: generation, PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Crosshost: &message, RecoveryCheckpoint: &metadata}
	}
	reserve := makeCommand("reserve", 1)
	if err = j.retainCheckpoint(reserve, d, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Accept(reserve); err != nil {
		t.Fatal(err)
	}
	body, _ := crossHostCommand(reserve)
	if _, err = j.reserveCrossHost(reserve, body); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Journal: j}
	if err = engine.finishCrossHost(reserve, body, deployment.Observation{}, "reserved", false); err != nil {
		t.Fatal(err)
	}
	r, _ := j.Record(reserve.Id)
	if err = j.Acknowledge(reserve.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	prepare := makeCommand("prepare", 2)
	unseen := makeCommand("hook", 3)
	if _, err = j.Accept(prepare); err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(t.TempDir(), "handoff")
	if err = j.ExportRecovery(export); err != nil {
		t.Fatal(err)
	}
	proofs, err := os.ReadFile(filepath.Join(export, "proofs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(proofs), "private-host-secret-canary") {
		t.Fatal("hydrated secrets escaped into recovery proofs")
	}
	evidence, err := j.RecoveryEvidence()
	if err != nil {
		t.Fatal(err)
	}
	if deployment.RecoveryDigest(proofs) != evidence.ProofDigest {
		t.Fatal("exported proof stream differs from its inventory digest")
	}
	h := deployment.RecoveryHandover{Authority: deployment.RecoveryAuthority{ParentID: parent, RootHash: d.RootHash, EvidenceHash: deployment.Hash(evidence), PriorController: 0, Controller: 1}, Descriptor: d, UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, UnitHash: p.Hash, Primary: true, Network: network}
	key := make([]byte, 32)
	if err = j.TransferRecovery("https://restored.example.test", key, strings.Repeat("t", 43), h); err != nil {
		t.Fatal(err)
	}
	if err = j.TransferRecovery("https://restored.example.test", key, strings.Repeat("u", 43), h); err != nil {
		t.Fatal("lost-response retry changed recovery authority", err)
	}
	dir := j.dir
	j.Close()
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	engine.Journal = j
	r, err = j.Record(prepare.Id)
	if err != nil || commandGeneration(r.Command) != 1 {
		t.Fatal("unfinished command did not retain its exact ID under current authority", err)
	}
	before := deployment.Hash(r)
	if err = engine.Execute(context.Background(), r.Command); err != nil {
		t.Fatal(err)
	}
	if err = engine.TickSchedules(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err = j.Record(prepare.Id)
	if err != nil || deployment.Hash(r) != before {
		t.Fatal("startup replay changed an operation while authority was held", err)
	}
	if _, err = j.Accept(prepare); err == nil {
		t.Fatal("the previous controller's command was accepted")
	}
	open := makeCommand("recovery-open", 30)
	open.Id = deployment.RecoveryOpenID(parent, p.UnitID, 1)
	generation := int64(1)
	open.ControllerGeneration = &generation
	stale := open
	stale.Id = deployment.RecoveryOpenID(parent, p.UnitID, 0)
	if err = engine.Execute(context.Background(), stale); err == nil {
		t.Fatal("prior grant ID was promoted to current authority")
	}
	if _, err = j.Record(stale.Id); !os.IsNotExist(err) {
		t.Fatal("stale replay grant entered journal", err)
	}
	wrong := open
	wrong.ResourceId = uuid.New()
	_ = engine.Execute(context.Background(), wrong)
	if _, err = j.Record(wrong.Id); !os.IsNotExist(err) {
		t.Fatal("wrong-unit recovery-open entered the command journal", err)
	}
	if held, err := j.recoveryHeld(); err != nil || !held {
		t.Fatal("a rejected open released the durable hold", err)
	}
	if err = engine.Execute(context.Background(), open); err != nil {
		t.Fatal(err)
	}
	if held, err := j.recoveryHeld(); err != nil || held {
		t.Fatal("current-authority open did not release replay hold", err)
	}
	state, err := j.crossHost()
	if err != nil || state.ParentID != parent || state.Unit.Plan.Hash != p.Hash || state.Controller != 1 || state.Phase != "reserved" {
		t.Fatal("recovery-open changed the frozen host reservation", err)
	}
	if err = j.RequireUnreservedHost(); err == nil {
		t.Fatal("authority recovery released the resource barrier")
	}
	opened, _ := j.Record(open.Id)
	if err = j.Acknowledge(open.Id, opened.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	r, err = j.Record(prepare.Id)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Mutate(prepare.Id, func(r *Record) { r.Report.State = "failed" }); err != nil {
		t.Fatal(err)
	}
	r, _ = j.Record(prepare.Id)
	if err = j.Acknowledge(prepare.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	unseen.ControllerGeneration = &generation
	if _, err = j.Accept(unseen); err != nil {
		t.Fatal("recovery-open made an unseen frozen child stale", err)
	}
}

func TestRecoveryHandoverLargeNetworkFencesAnUncontactedHost(t *testing.T) {
	j, p := testVisualJournal(t)
	cfg, _ := j.Config()
	cfg.MachineID = uuid.New()
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := j.saveUnit(unitState{Plan: p}); err != nil {
		t.Fatal(err)
	}
	policy := backup.Policy{ID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, Generation: 1, Enabled: true}
	if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: policy}}); err != nil {
		t.Fatal(err)
	}
	host := hostnetwork.Host{Dir: j.dir}
	member, peerMember, peerServer := uuid.New(), uuid.New(), uuid.New()
	own, err := host.Identity(member)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := host.Identity(peerMember)
	if err != nil {
		t.Fatal(err)
	}
	network := hostnetwork.Manifest{Version: 1, NetworkID: uuid.New(), MembershipID: member, ServerID: p.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: own.PublicKey, ListenPort: 51820, Peers: []hostnetwork.Peer{{ServerID: peerServer, MembershipID: peerMember, Address: "10.251.0.2", PublicKey: peer.PublicKey, Endpoint: "192.0.2.2:51820"}}}
	for i := 0; i < 100; i++ {
		network.Exports = append(network.Exports, hostnetwork.Endpoint{ID: uuid.New(), UnitID: p.UnitID, ResourceID: uuid.New(), InstanceID: uuid.New(), Port: 20000 + i, ContainerPort: 8080, Sources: []hostnetwork.Source{{ServerID: peerServer, UnitID: uuid.New()}}})
	}
	if err := network.Validate(); err != nil {
		t.Fatal(err)
	}
	parent, id := uuid.New(), uuid.New()
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: uuid.New(), GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest([]byte("opaque")), Bytes: 6, Participants: []uuid.UUID{p.ServerID, peerServer}}
	evidence, err := j.RecoveryEvidence()
	if err != nil {
		t.Fatal(err)
	}
	h := deployment.RecoveryHandover{Authority: deployment.RecoveryAuthority{ParentID: parent, RootHash: d.RootHash, EvidenceHash: deployment.Hash(evidence), Controller: 1}, Descriptor: d, UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, UnitHash: p.Hash, Network: network}
	raw, _ := json.Marshal(h)
	if len(raw) <= 32768 {
		t.Fatal("fixture does not cover an admitted network above the former handover cap")
	}
	if err = j.TransferRecovery("https://restored.example.test", make([]byte, 32), strings.Repeat("t", 43), h); err != nil {
		t.Fatal(err)
	}
	dir := j.dir
	j.Close()
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if barrier, err := j.hasCrossHostBarrier(); err != nil || barrier {
		t.Fatal("handover fabricated a resource reservation", err)
	}
	sentinel := recoverySentinelID(parent, p.ServerID)
	legacy, err := j.Record(sentinel)
	if err != nil || legacy.Acknowledged || legacy.Report.State != "needs_attention" {
		t.Fatal("a supported older reader has no pending barrier for the uncontacted host", err)
	}
	if err = j.Retry(sentinel); err == nil {
		t.Fatal("compatibility hold was retried as a deployment")
	}
	pending, err := j.Pending()
	if err != nil || len(pending) != 0 {
		t.Fatal("compatibility mirror escaped into current controller transport", err)
	}
	status, err := j.schedule(policy.ID)
	if err != nil || !status.Installation.Policy.Enabled {
		t.Fatal("handover changed the local policy instead of holding its execution", err)
	}
	if err = j.RequireUnreservedHost(); err == nil {
		t.Fatal("uncontacted host did not retain its recovery hold")
	}
	before, err := j.recoveredAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Transfer("https://other.example.test", make([]byte, 32), 2, strings.Repeat("u", 43)); err == nil {
		t.Fatal("ordinary transfer discarded a closed recovery hold")
	}
	after, err := j.recoveredAuthority()
	if err != nil || deployment.Hash(before) != deployment.Hash(after) {
		t.Fatal("rejected ordinary transfer changed recovered authority", err)
	}
	if _, err = j.Record(sentinel); err != nil {
		t.Fatal("rejected transfer discarded the compatibility barrier", err)
	}
}
