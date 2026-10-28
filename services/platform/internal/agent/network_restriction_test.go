package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

func TestRestrictiveNetworkRecordsFitJournalAndRecoveryTransport(t *testing.T) {
	j, plan := testVisualJournal(t)
	host := hostnetwork.Host{Dir: j.dir}
	member, peerMember := uuid.New(), uuid.New()
	identity, err := host.Identity(member)
	if err != nil {
		t.Fatal(err)
	}
	peerIdentity, err := host.Identity(peerMember)
	if err != nil {
		t.Fatal(err)
	}
	peer := uuid.New()
	before := hostnetwork.Manifest{Version: 1, NetworkID: uuid.New(), MembershipID: member, ServerID: plan.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: identity.PublicKey, ListenPort: 51820, Peers: []hostnetwork.Peer{{ServerID: peer, MembershipID: peerMember, Address: "10.251.0.2", PublicKey: peerIdentity.PublicKey, Endpoint: "192.0.2.2:51820"}}}
	for i := 0; ; i++ {
		index := i / 64
		if index == len(before.Exports) {
			before.Exports = append(before.Exports, hostnetwork.Endpoint{ID: uuid.New(), UnitID: plan.UnitID, ResourceID: uuid.New(), InstanceID: uuid.New(), Port: 20000 + index, ContainerPort: 8080})
		}
		before.Exports[index].Sources = append(before.Exports[index].Sources, hostnetwork.Source{ServerID: peer, UnitID: uuid.New()})
		raw, _ := json.Marshal(before)
		if len(raw) > 98304 {
			before.Exports[index].Sources = before.Exports[index].Sources[:len(before.Exports[index].Sources)-1]
			break
		}
	}
	if err := before.Validate(); err != nil {
		t.Fatal(err)
	}
	wanted := before
	wanted.Generation++
	wanted.Exports = append([]hostnetwork.Endpoint{}, before.Exports...)
	wanted.Exports[0].Sources = append([]hostnetwork.Source{}, before.Exports[0].Sources[1:]...)
	body := hostnetwork.Command{Action: "restrict", NetworkID: before.NetworkID, MembershipID: member, Baseline: &before, Manifest: &wanted}
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, Generation: 1, Kind: "network", Action: "inspect", ResourceId: before.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(time.Minute), Network: &message}
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	receipt := hostnetwork.Receipt{Identity: identity, NetworkID: before.NetworkID, Generation: wanted.Generation, Hash: hostnetwork.Hash(wanted), Before: &before, Effective: &wanted}
	receiptRaw, _ := json.Marshal(receipt)
	value := json.RawMessage(receiptRaw)
	if err := j.Mutate(c.Id, func(r *Record) { r.Report.State, r.Report.Network = "succeeded", &value }); err != nil {
		t.Fatal("admitted maximal restriction cannot persist its receipt", err)
	}
	r, err := j.Record(c.Id)
	if err != nil {
		t.Fatal("persisted maximal restriction cannot be read", err)
	}
	report, _ := json.Marshal(r.Report)
	record, _ := json.Marshal(r)
	header, _ := json.Marshal(r.Command)
	proof, _ := json.Marshal(deployment.RecoveryProof{ID: c.Id, Header: header, Report: report, Checkpoint: r.Checkpoint})
	if len(report) <= 196608 || len(report) >= 524288 || len(record) >= 524288 || len(proof)+1 >= 524288 {
		t.Fatalf("restriction transport bounds differ: report=%d record=%d proof=%d", len(report), len(record), len(proof)+1)
	}
	t.Logf("maximal restriction: report=%d journal=%d proof=%d", len(report), len(record), len(proof)+1)
	// A frozen removal may arrive after a newer network frame, but must not
	// lower the fence that rejects stale configure commands.
	if err := j.Acknowledge(c.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []int64{5, 2} {
		c.Id, c.Generation = uuid.New(), generation
		if _, err := j.Accept(c); err != nil {
			t.Fatal("restrictive intent was rejected", err)
		}
		if err := j.Mutate(c.Id, func(r *Record) { r.Report.State = "succeeded" }); err != nil {
			t.Fatal(err)
		}
		done, err := j.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Acknowledge(c.Id, done.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	body.Action, body.Baseline = "configure", nil
	raw, _ = json.Marshal(body)
	message = json.RawMessage(raw)
	c.Id, c.Generation, c.Network, c.PlanHash = uuid.New(), 4, &message, hostnetwork.Hash(body)
	if _, err := j.Accept(c); err == nil {
		t.Fatal("restrictive replay lowered the stale-configure fence")
	}
	body.Action, body.Baseline = "restrict", &before
	raw, _ = json.Marshal(body)
	message = json.RawMessage(raw)
	c.Id, c.Generation, c.Network, c.PlanHash = uuid.New(), 6, &message, hostnetwork.Hash(body)
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	if err := j.Transfer("https://transferred.example.test", make([]byte, 32), 1, strings.Repeat("t", 43)); err == nil {
		t.Fatal("ordinary transfer bypassed an unsettled removal without guarded outcome reconstruction")
	}
	preserved, err := j.Record(c.Id)
	if err != nil || preserved.Acknowledged || preserved.Report.State != "running" || commandGeneration(preserved.Command) != 0 || preserved.Command.PlanHash != c.PlanHash {
		t.Fatal("refused ordinary transfer changed the pending removal", err)
	}
}
