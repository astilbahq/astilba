package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

type preparationFixture struct {
	service                *fixture
	primary, child, parent uuid.UUID
	identity               execution.Identity
	job                    int64
	frozen                 []byte
	events                 <-chan *river.Event
}

func queuedPreparation(t *testing.T, primaryState, childState api.ExecutionState) preparationFixture {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	servers := []api.AgentEnrollment{}
	for range 2 {
		_, in, _ := enrollment(t, f, b, ws, "deployment")
		yes := true
		in.Capabilities.CrossHost = &yes
		if _, err := f.execution.Enroll(ctx, in); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, in)
	}
	if err := f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	identity, err := f.execution.Authenticate(ctx, servers[1].ServerId, servers[1].Credential)
	if err != nil {
		t.Fatal(err)
	}
	parent := uuid.New()
	commands := []api.ExecutionCommand{{Id: parent, ServerId: servers[0].ServerId, EnvironmentId: env, Kind: "crosshost", Generation: 1}}
	for i, server := range servers {
		body := deployment.CrossHostCommand{ParentID: parent, Phase: "prepare", Unit: deployment.DistributedUnit{Plan: deployment.Plan{
			ID: uuid.New(), ServerID: server.ServerId, EnvironmentID: env, UnitID: uuid.New(),
			Specification: domain.Specification{Placement: &domain.Placement{IngressServerID: servers[0].ServerId}},
			Local:         &deployment.LocalPlacement{Primary: i == 0},
		}}}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload := json.RawMessage(raw)
		commands = append(commands, api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: server.ServerId, EnvironmentId: env, Kind: "crosshost", Generation: int64(i + 2), PlanHash: body.Digest(), Crosshost: &payload, Deadline: time.Now().Add(time.Minute)})
	}
	states := []api.ExecutionState{"running", primaryState, childState}
	for i, command := range commands {
		raw, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,parent_id) VALUES($1,$2,$3,$4,$5,$6,$7,'crosshost',$8,$9)`, command.Id, ws, command.ServerId, env, command.Generation, uuid.New(), raw, states[i], command.ParentId); err != nil {
			t.Fatal(err)
		}
	}
	planID := uuid.New()
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) SELECT $1,e.workspace_id,$2,e.id,u.id,e.draft_revision_id,$3,'{}' FROM environments e JOIN deployment_units u ON u.workspace_id=e.workspace_id AND u.environment_id=e.id AND u.primary_unit WHERE e.workspace_id=$4 AND e.id=$5`, planID, servers[0].ServerId, parent, ws, env); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO cross_host_operations(id,workspace_id,environment_id,plan_id,phase) VALUES($1,$2,$3,$4,'prepare')`, parent, ws, env, planID); err != nil {
		t.Fatal(err)
	}
	job, err := f.execution.Queue.Insert(ctx, networkRemovalJob{ID: commands[2].Id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `UPDATE execution_operations SET job_id=$2 WHERE id=$1`, commands[2].Id, job.Job.ID); err != nil {
		t.Fatal(err)
	}
	var frozen []byte
	if err = f.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=$1`, commands[2].Id).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := f.execution.Queue.Subscribe(river.EventKindJobCompleted, river.EventKindJobSnoozed)
	t.Cleanup(unsubscribe)
	return preparationFixture{service: f, primary: commands[1].Id, child: commands[2].Id, parent: parent, identity: identity, job: job.Job.ID, frozen: frozen, events: events}
}

func (f preparationFixture) wait(t *testing.T, check func(api.ExecutionState, string) bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	var state api.ExecutionState
	var job string
	for {
		select {
		case event := <-f.events:
			if event.Job.ID != f.job {
				continue
			}
		case <-deadline.C:
			t.Fatalf("preparation did not reach the expected state: operation=%s job=%s", state, job)
		}
		var frozen []byte
		if err := f.service.st.Pool.QueryRow(context.Background(), `SELECT o.state,j.state,o.command FROM execution_operations o JOIN river_job j ON j.id=o.job_id WHERE o.id=$1`, f.child).Scan(&state, &job, &frozen); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(frozen, f.frozen) {
			t.Fatal("dispatch admission changed the frozen child command")
		}
		if check(state, job) {
			return
		}
	}
}

func TestCrossHostPreparationWaitsForPrimary(t *testing.T) {
	for _, state := range []api.ExecutionState{"running", "needs_attention"} {
		t.Run(string(state), func(t *testing.T) {
			f := queuedPreparation(t, state, "queued")
			ctx := context.Background()
			if err := f.service.execution.Start(ctx); err != nil {
				t.Fatal(err)
			}
			f.wait(t, func(state api.ExecutionState, _ string) bool { return state == "queued" })
			if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='succeeded' WHERE id=$1`, f.primary); err != nil {
				t.Fatal(err)
			}
			f.wait(t, func(state api.ExecutionState, _ string) bool { return state == "dispatched" })
			poll, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			command, err := f.service.execution.Poll(poll, f.identity)
			if err != nil || command == nil || command.Id != f.child {
				t.Fatal("successful primary preparation did not release its consumer", err)
			}
		})
	}
}

func TestCrossHostPreparationCancelsUnusedConsumer(t *testing.T) {
	for _, reason := range []string{"failed_primary", "cancelled_primary", "parent_cancel", "child_cancel"} {
		t.Run(reason, func(t *testing.T) {
			f := queuedPreparation(t, "running", "queued")
			ctx := context.Background()
			query, id := `UPDATE execution_operations SET cancel_requested=true WHERE id=$1`, f.parent
			if reason == "failed_primary" {
				query, id = `UPDATE execution_operations SET state='failed' WHERE id=$1`, f.primary
			} else if reason == "cancelled_primary" {
				query, id = `UPDATE execution_operations SET state='cancelled' WHERE id=$1`, f.primary
			} else if reason == "child_cancel" {
				id = f.child
			}
			if _, err := f.service.st.Pool.Exec(ctx, query, id); err != nil {
				t.Fatal(err)
			}
			if err := f.service.execution.Start(ctx); err != nil {
				t.Fatal(err)
			}
			f.wait(t, func(state api.ExecutionState, job string) bool { return state == "cancelled" && job == "completed" })
		})
	}
}

func TestCrossHostPreparationKeepsDispatchedReconciliation(t *testing.T) {
	f := queuedPreparation(t, "needs_attention", "dispatched")
	ctx := context.Background()
	if _, err := f.service.st.Pool.Exec(ctx, `UPDATE cross_host_operations SET phase='abort' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.execution.Start(ctx); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(state api.ExecutionState, _ string) bool { return state == "dispatched" })
	poll, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	command, err := f.service.execution.Poll(poll, f.identity)
	if err != nil || command == nil || command.Id != f.child {
		t.Fatal("existing host work lost its reconciliation delivery", err)
	}
}

func TestCrossHostPreparationDoesNotReplayObsoleteChild(t *testing.T) {
	f := queuedPreparation(t, "succeeded", "queued")
	ctx := context.Background()
	// An older prepare capsule can restore a queued child without its later
	// cancellation, while the subsequent capsule has already advanced to abort.
	if _, err := f.service.st.Pool.Exec(ctx, `UPDATE cross_host_operations SET phase='abort' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.execution.Start(ctx); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(state api.ExecutionState, job string) bool { return state == "cancelled" && job == "completed" })
	poll, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	command, err := f.service.execution.Poll(poll, f.identity)
	if command != nil || err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("obsolete restored preparation was delivered", command, err)
	}
}
