package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Poll never delivers a coordination root, so its agent cannot have an honest report for one.
// A cancelled root would complete without coordinating, leaving other hosts paused and reserved.
func TestAgentCannotReportCrossHostRoot(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := f.execution.Enroll(ctx, in); err != nil {
		t.Fatal(err)
	}
	identity, err := f.execution.Authenticate(ctx, in.ServerId, in.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	root := api.ExecutionCommand{Id: uuid.New(), ServerId: in.ServerId, EnvironmentId: env, Generation: 1, Kind: "crosshost", Action: "inspect", PlanHash: "root-plan-hash", Deadline: time.Now().Add(time.Hour)}
	raw, err := stablejson.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'running')`, root.Id, ws, root.ServerId, env, root.Generation, uuid.New(), raw, root.Kind); err != nil {
		t.Fatal(err)
	}
	// The identity, generation and plan hash all match: only the root's kind refuses this report.
	err = f.execution.Report(ctx, identity, api.AgentReport{Id: root.Id, Generation: root.Generation, PlanHash: root.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}, Note: "forged"})
	if !errors.Is(err, domain.Forbidden) {
		t.Fatalf("root report = %v, want forbidden", err)
	}
	var state, note string
	var sequence int64
	if err = f.st.Pool.QueryRow(ctx, `SELECT state,note,report_sequence FROM execution_operations WHERE id=$1`, root.Id).Scan(&state, &note, &sequence); err != nil {
		t.Fatal(err)
	}
	if state != "running" || note != "" || sequence != 0 {
		t.Fatalf("root changed: state=%q note=%q sequence=%d", state, note, sequence)
	}
}
