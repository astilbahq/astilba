package integration

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestVisualRuntimeOutcomeRequiresAppliedReport(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ctx := context.Background()
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Outcomes"}, 201, &project)
	var env api.Environment
	base := prefix + "/projects/" + project.Id.String() + "/environments"
	b.call("POST", base, map[string]string{"name": "Web"}, 201, &env)
	path := base + "/" + env.Id.String()
	image := execution.TestImage
	app := uuid.New()
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	server, input, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	input.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: server.Server.Id, BaseRevisionId: draft.Id}, 201, &plan)
	var polled api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	if polled.Command == nil {
		t.Fatal("resolve missing")
	}
	send := func(target *fixture, c api.ExecutionCommand, applied bool, resolve bool) {
		observation := deployment.Observation{ObservedAt: time.Now().UTC(), Applied: applied, Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}
		if resolve {
			observation.Images = map[string]string{app.String(): image}
		} else {
			observation.Resources = []deployment.Resource{{ID: app, State: "running", Image: image}}
		}
		raw, _ := stablejson.Marshal(observation)
		agentRequest(t, target.server.Client(), target.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
	}
	send(f, *polled.Command, false, true)
	b.call("GET", path+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
	var operation api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &operation)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	if polled.Command == nil || polled.Command.Id != operation.Id {
		t.Fatal("apply missing")
	}
	command := *polled.Command
	before := captureRecoveryOrderingSnapshot(t, f, ws)
	send(f, command, false, false)
	var falseOutcome *string
	if err := f.st.Pool.QueryRow(ctx, `SELECT runtime_outcome FROM execution_operations WHERE id=$1`, command.Id).Scan(&falseOutcome); err != nil || falseOutcome != nil {
		t.Fatal("unapplied success acquired an outcome", err, falseOutcome)
	}
	// Fork the same accepted command before its report. Internal snapshots omit
	// agent authentication, so copy that identity separately as fixture setup.
	fork := setup(t)
	useKeys(t, fork.st, f.st.Keys)
	if _, err := fork.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Outcome fork')`, ws); err != nil {
		t.Fatal(err)
	}
	tx, err := fork.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = portability.MergeRecovery(ctx, tx, before, fork.owner); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var credential []byte
	if err = f.st.Pool.QueryRow(ctx, `SELECT credential_hash FROM servers WHERE id=$1`, server.Server.Id).Scan(&credential); err != nil {
		t.Fatal(err)
	}
	if _, err = fork.st.Pool.Exec(ctx, `UPDATE servers SET credential_hash=$2 WHERE id=$1`, server.Server.Id, credential); err != nil {
		t.Fatal(err)
	}
	send(fork, command, true, false)
	var outcome string
	var runtimePlan, runtimeOperation uuid.UUID
	if err = fork.st.Pool.QueryRow(ctx, `SELECT o.runtime_outcome,o.runtime_plan_id,e.runtime_operation_id FROM execution_operations o JOIN environments e ON e.workspace_id=o.workspace_id AND e.id=o.environment_id WHERE o.id=$1`, command.Id).Scan(&outcome, &runtimePlan, &runtimeOperation); err != nil || outcome != "applied" || runtimePlan != plan.ID || runtimeOperation != command.Id {
		t.Fatal("actual application was not recorded atomically", err, outcome, runtimePlan, runtimeOperation)
	}
}
