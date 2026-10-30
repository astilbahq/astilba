package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

func TestPollDeliversRestrictionDuringPausedCrossHostOperation(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.CrossHost = &yes
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
	insert := func(c api.ExecutionCommand, state string, created time.Time) {
		t.Helper()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var environment *uuid.UUID
		if c.EnvironmentId != uuid.Nil {
			environment = &c.EnvironmentId
		}
		if _, err = f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,parent_id,generation,request_key,command,kind,state,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, c.Id, ws, c.ServerId, environment, c.ParentId, c.Generation, uuid.New(), raw, c.Kind, state, created); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	parent := api.ExecutionCommand{Id: uuid.New(), ServerId: in.ServerId, EnvironmentId: env, Generation: 1, Kind: "crosshost", Action: "inspect", Deadline: now.Add(time.Hour)}
	insert(parent, "needs_attention", now.Add(-4*time.Second))
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO cross_host_reservations(server_id,workspace_id,parent_id) VALUES($1,$2,$3)`, in.ServerId, ws, parent.Id); err != nil {
		t.Fatal(err)
	}
	child := parent
	child.Id, child.ParentId, child.Generation = uuid.New(), &parent.Id, 2
	body := deployment.CrossHostCommand{ParentID: parent.Id, Phase: "prepare", Unit: deployment.DistributedUnit{Plan: deployment.Plan{ServerID: in.ServerId, EnvironmentID: env}}}
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	child.Crosshost, child.PlanHash = &message, body.Digest()
	insert(child, "needs_attention", now.Add(-3*time.Second))

	restrictionBody := hostnetwork.Command{Action: "restrict", NetworkID: uuid.New(), MembershipID: uuid.New()}
	raw, _ = json.Marshal(restrictionBody)
	message = json.RawMessage(raw)
	restriction := api.ExecutionCommand{Id: uuid.New(), ServerId: in.ServerId, Generation: 3, Kind: "network", Action: "inspect", Network: &message, PlanHash: hostnetwork.Hash(restrictionBody), Deadline: now.Add(time.Hour)}
	insert(restriction, "dispatched", now.Add(-2*time.Second))

	checkpoint := child
	checkpoint.Id, checkpoint.Generation = uuid.New(), 4
	body.Phase = "checkpoint"
	raw, _ = json.Marshal(body)
	message = json.RawMessage(raw)
	checkpoint.Crosshost, checkpoint.PlanHash = &message, body.Digest()
	insert(checkpoint, "dispatched", now.Add(-time.Second))

	poll := func(want uuid.UUID) {
		t.Helper()
		command, err := f.execution.Poll(ctx, identity)
		if err != nil || command == nil {
			t.Fatalf("poll returned no command: %v; want %s", err, want)
		}
		if command.Id != want || command.CancelRequested {
			t.Fatalf("poll returned %s (%s, cancelled=%t); want uncancelled command %s", command.Id, command.Kind, command.CancelRequested, want)
		}
	}
	complete := func(id uuid.UUID) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='succeeded' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	// Checkpoints retain precedence, but a paused child must not starve removals.
	poll(checkpoint.Id)
	complete(checkpoint.Id)
	poll(restriction.Id)
	poll(restriction.Id)
	complete(restriction.Id)
	poll(child.Id)
	poll(child.Id)
}
