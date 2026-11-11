package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// Return to the outer peer loop after one bounded wait so heartbeats keep running.
func waitForFixturePollLease(ctx context.Context, err error) bool {
	if !errors.Is(err, execution.ErrPollLeaseHeld) {
		return false
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	return true
}

func TestFixturePollRecoversAfterLeaseContention(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	v, in, token := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	identity, err := f.execution.Authenticate(ctx, v.Server.Id, *in.Credential)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/workspaces/" + ws.String()
	var plan api.ExecutionPlan
	b.call("POST", base+"/plans", api.PlanRequest{ServerId: v.Server.Id, EnvironmentId: env, Action: "start"}, 201, &plan)
	var operation api.Operation
	b.call("POST", base+"/plans/"+plan.Id.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operation)
	for {
		var dispatched bool
		if err = f.st.Pool.QueryRow(ctx, "SELECT state='dispatched' FROM execution_operations WHERE id=$1", operation.Id).Scan(&dispatched); err != nil {
			t.Fatal(err)
		}
		if dispatched {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	lease := uuid.New()
	if _, err = f.st.Pool.Exec(ctx, "UPDATE servers SET poll_id=$2,poll_until=now()+interval '1 hour' WHERE id=$1", v.Server.Id, lease); err != nil {
		t.Fatal(err)
	}
	command, err := f.execution.Poll(ctx, identity)
	if command != nil || !errors.Is(err, execution.ErrPollLeaseHeld) || !errors.Is(err, domain.Conflict) {
		t.Fatal("lease contention lost its distinct conflict identity", command, err)
	}
	var response api.Error
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 409, &response)
	if response.Code != domain.Conflict.Code || response.Message != domain.Conflict.Message {
		t.Fatal("lease contention changed the public conflict response", response)
	}
	if !waitForFixturePollLease(ctx, err) || waitForFixturePollLease(ctx, domain.Conflict) {
		t.Fatal("fixture must retry only the distinct lease conflict")
	}
	if _, err = f.st.Pool.Exec(ctx, "UPDATE servers SET poll_until=now()-interval '1 second' WHERE id=$1 AND poll_id=$2", v.Server.Id, lease); err != nil {
		t.Fatal(err)
	}
	command, err = f.execution.Poll(ctx, identity)
	if err != nil || command == nil || command.Id != operation.Id {
		t.Fatal("expired lease did not release the original command", command, err)
	}
}

// A client can go away at any point in a poll, and an agent that restarted is the next to
// poll. A lease left behind would turn it away, with its commands and sessions, for as long
// as the lease lasts.
func TestCancelledPollLeavesNoLease(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, _ := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	identity, err := f.execution.Authenticate(t.Context(), v.Server.Id, *in.Credential)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := f.execution.PollAgent(ctx, identity)
			done <- err
		}()
		time.Sleep(time.Duration(i) * 15 * time.Microsecond)
		cancel()
		err := <-done
		var held bool
		if e := f.st.Pool.QueryRow(t.Context(), "SELECT poll_id IS NOT NULL FROM servers WHERE id=$1", v.Server.Id).Scan(&held); e != nil {
			t.Fatal(e)
		}
		if held {
			t.Fatalf("a poll cancelled after %dus left its lease, returning %v", i*15, err)
		}
	}
}

// An agent's polls carry a body, and net/http notices a client leaving only once a request's
// body has been read. Nothing in the poll reads it: the request validator happens to. Should
// that change, an agent that stopped would hold its own lease for a whole poll.
func TestDisconnectedPollLeavesNoLease(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, token := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	held := func() bool {
		var held bool
		if err := f.st.Pool.QueryRow(t.Context(), "SELECT poll_id IS NOT NULL FROM servers WHERE id=$1", v.Server.Id).Scan(&held); err != nil {
			t.Fatal(err)
		}
		return held
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/agent/v1/poll", strings.NewReader("null"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if response, err := f.server.Client().Do(request); err == nil {
			response.Body.Close()
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); !held(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the poll did not take its lease")
		}
	}
	cancel()
	<-done
	for deadline := time.Now().Add(5 * time.Second); held(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a poll whose client left kept its lease")
		}
	}
}
