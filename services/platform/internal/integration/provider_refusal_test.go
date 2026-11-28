package integration

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

// A Git provider that refuses a pipeline's repository for good, as once it is deleted, gives the
// same answer at every poll. The pipeline settles at the first, and the runner's later commands,
// which the poll serves after it, reach the runner.
func TestFinalProviderRefusalLetsTheRunnersLaterCommandsThrough(t *testing.T) {
	r := newRefusalRunner(t)
	var other struct {
		Connection pipeline.Connection `json:"connection"`
	}
	r.b.call("POST", r.prefix+"/git-connections", map[string]any{"name": "Other", "provider": "gitea", "baseUrl": r.provider.server.URL, "repository": "team/other", "clone": map[string]string{"username": "reader", "password": "clone-canary"}}, 201, &other)
	refused := r.createBuild(t, r.config)
	// The runner's next build, in another environment and repository, queues behind it.
	project := r.path[:strings.Index(r.path, "/environments/")]
	var env api.Environment
	r.b.call("POST", project+"/environments", map[string]string{"name": "Later"}, 201, &env)
	var draft api.Revision
	r.b.call("PUT", project+"/environments/"+env.Id.String()+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": r.spec}, 200, &draft)
	config := r.config
	config.ConnectionID = other.Connection.ID
	var later api.Operation
	r.b.call("POST", project+"/environments/"+env.Id.String()+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": r.app, "requestKey": uuid.New(), "configuration": config}, 202, &later)
	if state, _, _ := r.operation(t, later.Id); state != "queued" {
		t.Fatal("the later build did not queue behind the first:", state)
	}

	refuseRepository(http.StatusNotFound, nil, `{"message":"Not Found"}`)(r)
	r.deliverWithValues(t, later.Id)
	const note = "Refused before runner delivery: the Git provider has no repository by that name that the connection can see (HTTP 404): it was deleted, renamed or made private, or the connection lost access. Check the repository and the connection, then start this again."
	if state, got, _ := r.operation(t, refused.Id); state != "failed" || got != note {
		t.Fatalf("the refused build ended %s with the note %q", state, got)
	}
	// Settled once: no later poll asks about it again.
	asked := r.provider.requests("/repos/team/app")
	r.poll(t, 200)
	if r.provider.requests("/repos/team/app") != asked {
		t.Fatal("a settled build's repository was asked about again")
	}
	if state, got, _ := r.operation(t, refused.Id); state != "failed" || got != note {
		t.Fatalf("the settled build changed to %s with the note %q", state, got)
	}
}

// A rate limit passes, so a build refused under one waits, queued or held, neither failed nor
// stopped, and is delivered once the provider answers again. GitHub, Gitea and GitLab refuse over
// their limits with 403 as well as 429.
func TestRateLimitedProviderKeepsTheBuildWaiting(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)
	for _, tc := range []struct {
		name   string
		status int
		header map[string]string
		body   string
	}{
		{name: "403 with retry-after", status: http.StatusForbidden, header: map[string]string{"Retry-After": "60"}, body: `{"message":"forbidden"}`},
		{name: "403 with a used-up limit", status: http.StatusForbidden, header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}},
		{name: "403 with GitLab's used-up limit", status: http.StatusForbidden, header: map[string]string{"RateLimit-Remaining": "0", "RateLimit-Reset": reset}},
		{name: "403 saying so", status: http.StatusForbidden, body: `{"message":"API rate limit exceeded for 203.0.113.7."}`},
		{name: "429", status: http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRefusalRunner(t)
			waits := func(id uuid.UUID) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command, err := pollReleased(r.f.execution, ctx, r.identity)
				var refusal *pipeline.ProviderError
				if !errors.As(err, &refusal) || !refusal.Limited || carriesCredential(command) {
					t.Fatal("a rate-limited delivery did not wait:", err)
				}
				if state, note, cancelled := r.operation(t, id); state != "dispatched" || note != "" || cancelled {
					t.Fatalf("a rate-limited build is %s, cancel requested %v, with the note %q", state, cancelled, note)
				}
			}
			refuse, restore := refuseRepository(tc.status, tc.header, tc.body), func() { r.provider.refusing.Store(nil) }

			queued := r.createBuild(t, r.config)
			refuse(r)
			waits(queued.Id)
			restore()
			delivered := r.deliverWithValues(t, queued.Id)

			// Possibly held: the runner asks for the build's values again under the limit.
			refuse(r)
			agentRequest(t, r.f.server.Client(), r.f.server.URL, "/agent/v1/releases", r.token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: delivered.Id, Epoch: delivered.SecretRelease.Epoch, Reason: api.JournalMissing}}}, 204, nil)
			waits(delivered.Id)
			restore()
			r.report(t, r.deliverWithValues(t, delivered.Id), "cancelled")
		})
	}
}

// The poll asks a dispatched preview's pull request before serving it. A request the provider has
// no record of any more, as once the repository is deleted, stops the preview, as a closed one
// does, rather than failing every poll by its host. A rate limit passes, so the preview waits.
func TestPreviewWhosePullRequestIsGoneStopsAtThePoll(t *testing.T) {
	t.Run("gone", func(t *testing.T) {
		h, _ := approvedPreviewBuild(t)
		build := nextPreviewCommand(t, h)
		h.provider.refusing.Store(&providerRefusal{match: "/pulls/7", status: http.StatusNotFound, body: `{"message":"Not Found"}`})
		stop := nextPreviewCommand(t, h)
		if stop.Id != build.Id || !stop.CancelRequested {
			t.Fatal("a preview whose request is gone was not stopped", stop.Id, stop.CancelRequested)
		}
	})
	t.Run("rate limited", func(t *testing.T) {
		h, _ := approvedPreviewBuild(t)
		build := nextPreviewCommand(t, h)
		h.provider.refusing.Store(&providerRefusal{match: "/pulls/7", status: http.StatusForbidden, header: map[string]string{"Retry-After": "60"}})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		var refusal *pipeline.ProviderError
		if _, err := pollReleased(h.f.execution, ctx, h.identity); !errors.As(err, &refusal) || !refusal.Limited {
			t.Fatal("a rate-limited pull request check did not wait:", err)
		}
		var cancelled bool
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT cancel_requested FROM execution_operations WHERE id=$1", build.Id).Scan(&cancelled); err != nil || cancelled {
			t.Fatal("a rate-limited preview was stopped", cancelled, err)
		}
		h.provider.refusing.Store(nil)
		if again := nextPreviewCommand(t, h); again.Id != build.Id || again.CancelRequested {
			t.Fatal("the preview did not resume once the limit passed", again.Id, again.CancelRequested)
		}
	})
}
