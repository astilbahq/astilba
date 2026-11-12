package agent

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Retirement removes the environment's finished command records, which hold the secret values its
// deployments were sent, and a removed command delivered again is still never run. A report not yet
// acknowledged, other environments' records and the retire command's own record stay, and a
// cross-host phase record, which recovery reads, stays without its secrets.
func TestRetirementForgetsTheEnvironmentsFinishedRecords(t *testing.T) {
	j, plan := testVisualJournal(t)
	withSecret := func(c api.ExecutionCommand) api.ExecutionCommand {
		var p deployment.Payload
		if err := json.Unmarshal(*c.Visual, &p); err != nil {
			t.Fatal(err)
		}
		p.Secrets = map[string]string{uuid.NewString(): "retired-secret-value"}
		raw, _ := stablejson.Marshal(p)
		value := jsontext.Value(raw)
		c.Visual = &value
		return c
	}
	deployed := finished(t, j, withSecret(visualCommand(plan, "deploy", 1))).Command
	other := plan
	other.EnvironmentID, other.UnitID = uuid.New(), uuid.New()
	kept := finished(t, j, withSecret(visualCommand(other, "deploy", 1))).Command

	// A coordinated phase record from an earlier cross-host deployment, and a finished report
	// management has not acknowledged yet.
	parent := uuid.New()
	phase := deployment.CrossHostCommand{ParentID: parent, GroupHash: strings.Repeat("a", 64), Phase: "complete", Unit: deployment.DistributedUnit{Plan: plan}, Secrets: map[string]string{uuid.NewString(): "retired-secret-value"}}
	raw, _ := stablejson.Marshal(phase)
	body := jsontext.Value(raw)
	coordinated := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Kind: "crosshost", Crosshost: &body, PlanHash: phase.Digest(), Generation: 1}
	unreported := withSecret(visualCommand(plan, "deploy", 1))
	put := func(r Record) {
		t.Helper()
		if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "operations", r.Command.Id.String(), r) }); err != nil {
			t.Fatal(err)
		}
	}
	put(Record{Command: coordinated, Report: api.AgentReport{Id: coordinated.Id, State: "succeeded", Sequence: 2}, Acknowledged: true})

	retire := visualCommand(plan, "retire", 2)
	if _, err := j.Accept(retire); err != nil {
		t.Fatal(err)
	}
	put(Record{Command: unreported, Report: api.AgentReport{Id: unreported.Id, State: "failed", Sequence: 2}})
	if err := j.retireUnit(retire); err != nil {
		t.Fatal(err)
	}

	if _, err := j.Record(deployed.Id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the environment's finished deployment record was kept", err)
	}
	for _, id := range []uuid.UUID{kept.Id, retire.Id, unreported.Id} {
		if _, err := j.Record(id); err != nil {
			t.Fatal("a record retirement must keep was removed", err)
		}
	}
	r, err := j.Record(coordinated.Id)
	if err != nil {
		t.Fatal("the coordinated phase record was removed", err)
	}
	if got, err := crossHostCommand(r.Command); err != nil || len(got.Secrets) != 0 || got.Unit.Plan.Hash != plan.Hash {
		t.Fatalf("the coordinated phase record kept its secrets or changed: %v %+v", err, got.Secrets)
	}
	if !execution.Terminal(r.Report.State) || !r.Acknowledged {
		t.Fatal("redaction changed the record's outcome")
	}
	// The scope's forgotten ceiling covers it, so delivered again it is refused rather than run or
	// called unrun.
	var ceiling int64
	if err := j.db.View(func(tx *bolt.Tx) error {
		return j.get(tx, "generations", "forgotten:"+plan.EnvironmentID.String(), &ceiling)
	}); err != nil || ceiling < deployed.Generation {
		t.Fatal("the removed deployment is not below the forgotten ceiling", ceiling, err)
	}
}

// Retirement drops the credentials the environment's ended upgrades kept, even while the host's own
// clock says their retention has not ended: management has already waited for it. An upgrade that
// is still running and another environment's keep theirs.
func TestRetirementDropsEndedUpgradeCredentials(t *testing.T) {
	j, plan := testVisualJournal(t)
	saved := func(environment uuid.UUID, phase string) uuid.UUID {
		t.Helper()
		s := upgradeState{Command: upgrade.Command{Plan: upgrade.Plan{ID: uuid.New()}, Installation: backup.Installation{Policy: backup.Policy{ServerID: plan.ServerID, EnvironmentID: environment}, Access: backup.Access{Password: "retired-repository-password"}}, Secrets: map[string]string{uuid.NewString(): "retired-secret-value"}},
			Status: upgrade.Status{Phase: phase, KeepUntil: time.Now().Add(24 * time.Hour)}}
		if err := j.saveUpgrade(s); err != nil {
			t.Fatal(err)
		}
		return s.Command.Plan.ID
	}
	ended, running, other := saved(plan.EnvironmentID, "complete"), saved(plan.EnvironmentID, "running"), saved(uuid.New(), "complete")
	retire := visualCommand(plan, "retire", 2)
	if _, err := j.Accept(retire); err != nil {
		t.Fatal(err)
	}
	if err := j.retireUnit(retire); err != nil {
		t.Fatal(err)
	}
	for id, kept := range map[uuid.UUID]bool{ended: false, running: true, other: true} {
		s, err := j.upgrade(id)
		if err != nil {
			t.Fatal(err)
		}
		if held := len(s.Command.Secrets) > 0 || s.Command.Installation.Access != (backup.Access{}); held != kept {
			t.Fatalf("upgrade %s in phase %s kept its credentials: %t, want %t", id, s.Status.Phase, held, kept)
		}
	}
}
