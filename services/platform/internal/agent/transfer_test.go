package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"reflect"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
)

func TestControllerTransferPersistsFenceAndRetainsLocalState(t *testing.T) {
	j, plan := testVisualJournal(t)
	key, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cfg, _ := j.Config()
	cfg.Origin = "https://old.example.test"
	cfg.Enrolled = true
	cfg.Credential = "old-credential"
	cfg.MachineID = uuid.New()
	cfg.PublicKey = key
	if e = j.SetConfig(cfg); e != nil {
		t.Fatal(e)
	}
	if e = j.saveUnit(unitState{Plan: plan, Expected: map[string]string{"service": "container-id"}}); e != nil {
		t.Fatal(e)
	}
	policy := backup.Policy{ID: uuid.New(), ServerID: plan.ServerID, EnvironmentID: plan.EnvironmentID, Generation: 7}
	if e = j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: policy}}); e != nil {
		t.Fatal(e)
	}
	before, e := j.Inventory()
	if e != nil {
		t.Fatal(e)
	}
	command := visualCommand(plan, "observe", 1)
	if _, e = j.Accept(command); e != nil {
		t.Fatal(e)
	}
	if e = j.Transfer("https://new.example.test", key, 1, strings.Repeat("t", 43)); e == nil {
		t.Fatal("transferred with unacknowledged work")
	}
	if e = j.Mutate(command.Id, func(r *Record) { r.Report.State = "succeeded" }); e != nil {
		t.Fatal(e)
	}

	// Opening the journal models a second transfer CLI while the daemon owns it.
	if other, e := Open(j.dir); e == nil {
		other.Close()
		t.Fatal("live journal lock was bypassed")
	}
	if e = j.Transfer("https://new.example.test", key, 1, strings.Repeat("t", 43)); e != nil {
		t.Fatal(e)
	}
	if e = j.Transfer("https://new.example.test", key, 1, strings.Repeat("t", 43)); e != nil {
		t.Fatal("retry failed", e)
	}
	dir := j.dir
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	j, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	after, e := j.Inventory()
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("local units/schedules changed: %v", e)
	}
	saved, e := j.Config()
	if e != nil || saved.ControllerGeneration != 1 || saved.Origin != "https://new.example.test" || saved.Credential != "" || saved.Pending != "" || saved.Enrolled || saved.EnrollmentToken != strings.Repeat("t", 43) || saved.MachineID != cfg.MachineID {
		t.Fatalf("trust replacement not durable: %v", e)
	}
	command.Id = uuid.New()
	command.Generation = 999
	if _, e = j.Accept(command); e == nil {
		t.Fatal("old controller work accepted with a higher operation generation")
	}
	generation := int64(1)
	command.ControllerGeneration = &generation
	// A management restore may reuse a counter already seen from the old
	// controller. The new authority must still be able to resume execution.
	command.Generation = 1
	if _, e = j.Accept(command); e != nil {
		t.Fatal("new controller command refused", e)
	}
}

func TestExpiredVisualCancellationRetainsHydratedState(t *testing.T) {
	j, plan := testVisualJournal(t)
	command := visualCommand(plan, "deploy", 1)
	var payload deployment.Payload
	if e := json.Unmarshal(*command.Visual, &payload); e != nil {
		t.Fatal(e)
	}
	payload.Secrets = map[string]string{uuid.NewString(): "stored-secret"}
	raw, _ := json.Marshal(payload)
	message := json.RawMessage(raw)
	command.Visual = &message
	if _, e := j.Accept(command); e != nil {
		t.Fatal(e)
	}
	payload.Secrets = nil
	raw, _ = json.Marshal(payload)
	cancelPayload := json.RawMessage(raw)
	cancelled := command
	cancelled.Visual = &cancelPayload
	cancelled.CancelRequested = true
	record, e := j.Accept(cancelled)
	if e != nil || !record.Command.CancelRequested {
		t.Fatal("credential-free cancellation rejected", e)
	}
	if e = json.Unmarshal(*record.Command.Visual, &payload); e != nil || len(payload.Secrets) != 1 {
		t.Fatal("cancellation erased locally retained recovery secrets", e)
	}
}
