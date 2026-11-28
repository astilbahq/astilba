package integration

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Management's own configuration can refuse a pipeline's connection: an operator removes its
// provider's origin, or management starts without its GitHub App, or with another. Only the
// operator's change undoes that, so the pipeline waits for it rather than settling. It waits queued,
// saying why, so its runner's other commands, its update included, are dispatched and delivered
// meanwhile, and once the configuration allows it again it is delivered. A refusal that outlasts
// the pipeline's deadline ends it as an expiry, still saying why it waited.
func TestOperatorRefusalWaitsQueuedWhileTheRunnersOtherCommandsGo(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  bool
		note string
		// refuse changes management's configuration once the build is dispatched, and restore,
		// when set, puts it right while the build waits.
		refuse, restore func(r *refusalRunner)
	}{
		{
			// The fixture's provider is a loopback address that only development allows.
			name:   "provider origin removed",
			note:   "Delivery waited because management's configuration does not allow this connection's Git provider",
			refuse: func(r *refusalRunner) { r.f.execution.Policy.Development = false },
		},
		{
			name:   "management without its GitHub App",
			app:    true,
			note:   "Delivery waited because management is running without the GitHub App this connection uses.",
			refuse: func(r *refusalRunner) { r.f.execution.GitHub = nil },
		},
		{
			// A binding made for another App than management's, as when management restarts with
			// another App's configuration.
			name: "management with another GitHub App",
			app:  true,
			note: "Delivery waited because management is running with another GitHub App than the one this connection was authorized with.",
			refuse: func(r *refusalRunner) {
				execSQL(t, r.hungBuild, "UPDATE github_app_connections SET app_id=app_id+1 WHERE connection_id=$1", r.connection.ID)
			},
			restore: func(r *refusalRunner) {
				execSQL(t, r.hungBuild, "UPDATE github_app_connections SET app_id=app_id-1 WHERE connection_id=$1", r.connection.ID)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRefusalRunner(t)
			// An agent whose release the catalog knows, so it can be updated.
			r.capabilities.Version = "0.1.0"
			r.f.execution.Releases = []api.AgentRelease{catalogRelease("0.2.0", "0.1.0", "arm64"), catalogRelease("0.1.0", "0.0.0", "arm64")}
			r.heartbeat(t)
			config := r.config
			if tc.app {
				config.ConnectionID = heldGitHubFor(t, r.f, r.b, r.ws).connect(t)
				r.connection.ID = config.ConnectionID
			}
			op := r.createBuild(t, config)
			// Nothing reads management's configuration for a dispatched build until a poll does.
			tc.refuse(r)

			// The poll answers, and hands the build back to the queue.
			if c := r.poll(t, 200); c != nil {
				t.Fatal("the runner was given a command management's configuration refuses:", c.Id)
			}
			waiting := func() {
				t.Helper()
				state, note, cancelled := r.operation(t, op.Id)
				if state != "queued" || cancelled || strings.Count(note, tc.note) != 1 || r.deliveryStarted(t, op.Id) {
					t.Fatalf("a build waiting for management's configuration is %s, cancel requested %v, with the note %q", state, cancelled, note)
				}
			}
			waiting()

			// The runner's turn is free, so its update is taken, dispatched and delivered.
			var update api.Operation
			r.b.call("POST", r.prefix+"/servers/"+r.server.String()+"/update", api.UpdateAgent{Version: "0.2.0"}, 201, &update)
			updating := r.next(t)
			if updating.Id != update.Id || updating.Kind != "update" {
				t.Fatal("the runner's update was not delivered past the waiting build:", updating.Id, updating.Kind)
			}
			r.report(t, updating, "succeeded")
			// The dispatcher keeps the build queued, rather than giving it the runner's turn back. No poll
			// comes first, which would hand a dispatched build back to the queue itself.
			r.dispatcherPassed(t, op.Id)
			waiting()

			if tc.restore != nil {
				tc.restore(r)
				delivered := r.next(t)
				if delivered.Id != op.Id || delivered.CancelRequested || delivered.SecretRelease == nil || !delivered.SecretRelease.Values {
					t.Fatal("the build was not delivered once management's configuration allowed it", delivered.Id)
				}
				if _, note, _ := r.operation(t, op.Id); strings.Count(note, tc.note) != 1 {
					t.Fatalf("the delivered build lost why it waited: %q", note)
				}
				return
			}
			// Its deadline passes while it waits.
			execSQL(t, r.hungBuild, "UPDATE execution_operations SET command=jsonb_set(command,'{deadline}',to_jsonb(now()-interval '1 minute')) WHERE id=$1", op.Id)
			r.waitState(t, op.Id, "cancelled")
			if _, note, _ := r.operation(t, op.Id); !strings.HasPrefix(note, "Cancelled or expired before dispatch. ") || strings.Count(note, tc.note) != 1 {
				t.Fatalf("the expired build's note is %q", note)
			}
		})
	}
}

// A build its runner may already hold keeps its place when management's configuration refuses
// it: it goes without its values, says why, and the poll answers.
func TestOperatorRefusalWithholdsAPossiblyHeldBuildsValues(t *testing.T) {
	r := newRefusalRunner(t)
	delivered := r.deliverWithValues(t, r.createBuild(t, r.config).Id)
	r.f.execution.Policy.Development = false
	// The runner lost the build, and asks for its values again.
	agentRequest(t, r.f.server.Client(), r.f.server.URL, "/agent/v1/releases", r.token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: delivered.Id, Epoch: delivered.SecretRelease.Epoch, Reason: api.JournalMissing}}}, 204, nil)
	if c := r.poll(t, 200); c != nil {
		t.Fatal("the runner was given a command management's configuration refuses:", c.Id)
	}
	state, note, cancelled := r.operation(t, delivered.Id)
	if state != "dispatched" || cancelled || !strings.Contains(note, "Delivery waited because management's configuration does not allow this connection's Git provider") {
		t.Fatalf("a possibly held build waiting for management's configuration is %s, cancel requested %v, with the note %q", state, cancelled, note)
	}
}

// dispatcherPassed waits until the dispatcher has finished a pass over the operation's job that
// began after the runner's turn was free for it.
func (r *refusalRunner) dispatcherPassed(t *testing.T, operation uuid.UUID) {
	t.Helper()
	const attempted = "SELECT j.attempted_at FROM river_job j JOIN execution_operations o ON o.job_id=j.id WHERE o.id=$1 AND j.state<>'running'"
	var since time.Time
	if err := r.f.st.Pool.QueryRow(t.Context(), "SELECT now()").Scan(&since); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var at *time.Time
		if err := r.f.st.Pool.QueryRow(t.Context(), attempted, operation).Scan(&at); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if at != nil && at.After(since) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the dispatcher did not work the build's job again")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The poll asks a dispatched preview's pull request before serving it. Management's own
// configuration refusing the preview's connection is not the request's answer, so the preview's
// build waits for it, queued, as any build does, and the poll by its host answers.
func TestOperatorRefusalHoldsAPreviewAtThePoll(t *testing.T) {
	h, preview := approvedPreviewBuild(t)
	h.f.execution.Policy.Development = false
	var out api.AgentPoll
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", h.token, nil, 200, &out)
	if out.Command != nil {
		t.Fatal("the preview's host was given a command management's configuration refuses:", out.Command.Id)
	}
	var state, note string
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note FROM execution_operations WHERE id=$1", *preview.OperationId).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || !strings.Contains(note, "Delivery waited because management's configuration does not allow this connection's Git provider") {
		t.Fatalf("a preview build waiting for management's configuration is %s with the note %q", state, note)
	}
}
