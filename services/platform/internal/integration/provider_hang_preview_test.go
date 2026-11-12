package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/google/uuid"
)

type hungPreview struct {
	*hungBuild
	policy map[string]any
}

func newHungPreview(t *testing.T) *hungPreview {
	t.Helper()
	h := newHungBuild(t)
	var project api.Project
	h.b.call("POST", h.prefix+"/projects", map[string]string{"name": "Previews"}, 201, &project)
	var template api.Environment
	h.b.call("POST", h.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "PR template", "purpose": "preview_template"}, 201, &template)
	h.path = h.prefix + "/projects/" + project.Id.String() + "/environments/" + template.Id.String()
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": template.DraftRevisionId, "specification": h.spec}, 200, &h.draft)
	h.server, h.token, h.enrolled = enrollPreviewHost(t, h, "Preview host")
	var err error
	if h.identity, err = h.f.execution.Authenticate(t.Context(), h.server, *h.enrolled.Credential); err != nil {
		t.Fatal(err)
	}
	build := pipeline.BuildConfig{Builder: "dockerfile", ConnectionID: h.connection.ID, Branch: "main", RunnerID: h.server, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, Budget: &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 512, Minutes: 5}}
	return &hungPreview{h, map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "build": build, "maxActive": 1, "maxConcurrentBuilds": 1, "limits": map[string]int{"cpus": 1, "memoryMiB": 256, "pids": 64}, "closeGraceHours": 24, "generatedDomains": false}}
}

// enrollPreviewHost enrolls a clean preview-only server and returns its id, agent token and enrollment.
func enrollPreviewHost(t *testing.T, h *hungBuild, name string) (uuid.UUID, string, api.AgentEnrollment) {
	t.Helper()
	var enrollment api.Enrollment
	h.b.call("POST", h.prefix+"/servers", map[string]any{"name": name, "role": "combined", "previewOnly": true}, 201, &enrollment)
	credential, _ := randtoken.New()
	yes := true
	input := api.AgentEnrollment{ServerId: enrollment.Server.Id, Token: enrollment.Token, Credential: &credential, MachineId: uuid.New(), Capabilities: api.AgentCapabilities{Protocol: 1, Version: "preview-fixture", Os: "linux", Arch: "arm64", Docker: true, Build: &yes, BuildNetwork: &yes, BuildControls: &yes, BuildCache: &yes, Visual: &yes, Previews: &yes, ServiceOutputs: &yes, SafeReleases: &yes, CredentialFreeVisualCancel: &yes}, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 8 << 30, Notes: []string{}}}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	return enrollment.Server.Id, enrollment.Server.Id.String() + "." + credential, input
}

// Saving a policy checks its branch while holding the runner's row: the original report of
// this defect. The runner must keep polling and heartbeating during the call, and the save
// must answer to the draft and the runner as they stand afterwards.
func TestPreviewPolicyRechecksAfterAHungProvider(t *testing.T) {
	policies := func(t *testing.T, h *hungPreview) int {
		t.Helper()
		var count int
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM preview_policies WHERE workspace_id=$1", h.ws).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for _, tc := range []struct {
		name     string
		during   func(t *testing.T, h *hungPreview)
		status   int
		policies int
	}{
		{
			name: "owner demoted",
			during: func(t *testing.T, h *hungPreview) {
				execSQL(t, h.hungBuild, "UPDATE memberships SET role='viewer' WHERE workspace_id=$1", h.ws)
			},
			status: 403,
		},
		{
			name:   "provider failed",
			during: func(t *testing.T, h *hungPreview) { h.provider.failReleased() },
			status: 400,
		},
		{name: "nothing changed", during: func(*testing.T, *hungPreview) {}, status: 200, policies: 1},
		{
			name: "draft replaced",
			during: func(t *testing.T, h *hungPreview) {
				h.spec.Applications[0].Name = "edited"
				h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": h.spec}, 200, nil)
			},
			status: 409,
		},
		{
			name: "runner revoked",
			during: func(t *testing.T, h *hungPreview) {
				execSQL(t, h.hungBuild, "UPDATE servers SET revoked=true WHERE id=$1", h.server)
			},
			status: 404,
		},
		{
			name: "connection credential replaced",
			during: func(t *testing.T, h *hungPreview) {
				// Unreadable, so only asking again with this credential can notice it. The
				// answer from the old credential would have let the save through.
				execSQL(t, h.hungBuild, `UPDATE git_connections SET encrypted_access='\\x00' WHERE id=$1`, h.connection.ID)
			},
			status: 500,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungPreview(t)
			entered, release := h.provider.hold(t, "/branches/main")
			finished := h.b.send("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy})
			waitEntered(t, entered)
			h.requireNothingHeld(t, uuid.Nil)
			// The reported failure: a poll behind the held row gave up after five seconds.
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			started := time.Now()
			if _, err := pollReleased(h.f.execution, ctx, h.identity); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("the runner's poll did not run during the call:", err)
			}
			if elapsed := time.Since(started); elapsed > 4*time.Second {
				t.Fatal("the runner's poll waited behind the provider call", elapsed)
			}
			tc.during(t, h)
			release()
			requireReply(t, <-finished, tc.status)
			if got := policies(t, h); got != tc.policies {
				t.Fatal("policies saved", got, "want", tc.policies)
			}
			// Only the pass that saves is audited, once, however many passes it took.
			var audits int
			if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='preview.policy'", h.ws).Scan(&audits); err != nil || audits != tc.policies {
				t.Fatal("audit records", audits, "want", tc.policies, err)
			}
		})
	}
}

// The generation stays the same when a policy is paused, so that alone cannot show an owner
// paused it while another's save waited on the provider. That save must not undo the pause.
func TestPolicySaveDoesNotUndoAPauseDuringItsCheck(t *testing.T) {
	h := newHungPreview(t)
	var saved api.PreviewPolicy
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, &saved)
	entered, release := h.provider.hold(t, "/branches/main")
	finished := h.b.send("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": saved.Generation, "configuration": h.policy})
	waitEntered(t, entered)
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": false, "generation": saved.Generation, "configuration": h.policy}, 200, nil)
	release()
	requireReply(t, <-finished, 409)
	var enabled bool
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT enabled FROM preview_policies WHERE workspace_id=$1", h.ws).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("a save that started before the pause undid it")
	}
}

// One tenant's hung requests must leave the pool to everyone else, however many there are,
// and the writers they were racing still serialize: the saves all asked at generation 0,
// and the generation read under the lock lets only one of them through.
func TestHungPolicySavesLeaveThePoolServing(t *testing.T) {
	h := newHungPreview(t)
	entered, release := h.provider.hold(t, "/branches/main")
	size := int(h.f.st.Pool.Config().MaxConns) + 2
	results := make([]<-chan reply, size)
	for i := range results {
		results[i] = h.b.send("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy})
	}
	for range size {
		waitEntered(t, entered)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// River keeps a listener and may be running a job, so leave it room.
	for range h.f.st.Pool.Config().MaxConns - 2 {
		conn, err := h.f.st.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal("hung provider calls pinned the pool:", err)
		}
		defer conn.Release()
	}
	h.requireNothingHeld(t, uuid.Nil)
	h.b.call("POST", h.prefix+"/projects", map[string]string{"name": "Written during the hang"}, 201, nil)
	release()
	saved, conflicted := 0, 0
	for _, result := range results {
		r := <-result
		switch {
		case r.err == nil && r.status == 200:
			saved++
		case r.err == nil && r.status == 409:
			conflicted++
		default:
			t.Fatal("a racing save failed", r.status, string(r.body), r.err)
		}
	}
	if saved != 1 || conflicted != size-1 {
		t.Fatal("racing saves", saved, conflicted)
	}
}

// Approving a fork's commit grants it credentials, so the approval must be for the commit
// the request is at, and the policy it is under, when the approval commits.
func TestPreviewApprovalRechecksAfterAHungProvider(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during string
	}{
		{name: "head moved", during: "UPDATE preview_requests SET head=repeat('d',40) WHERE id=$1"},
		{name: "policy saved", during: "UPDATE preview_policies SET generation=generation+1 WHERE id=(SELECT policy_id FROM preview_requests WHERE id=$1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungPreview(t)
			h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
			preview := h.waitPreview(t, "approval_required")
			entered, release := h.provider.hold(t, "/pulls/7")
			finished := h.b.send("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository})
			waitEntered(t, entered)
			h.requireNothingHeld(t, uuid.Nil)
			execSQL(t, h.hungBuild, tc.during, preview.Id)
			release()
			requireReply(t, <-finished, 409)
			var approved string
			if err := h.f.st.Pool.QueryRow(context.Background(), "SELECT approved_head FROM preview_requests WHERE id=$1", preview.Id).Scan(&approved); err != nil {
				t.Fatal(err)
			}
			if approved != "" {
				t.Fatal("an approval applied to a state that changed during the call", approved)
			}
		})
	}
}

// A preview job checks its request with the provider. Authority to run it must be the
// authority after the call, not before.
func TestPreviewWorkerRechecksAuthorityAfterAHungProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		during   func(t *testing.T, h *hungPreview)
		requests int
	}{
		{name: "nothing changed", during: func(*testing.T, *hungPreview) {}, requests: 1},
		{
			name: "owner demoted",
			during: func(t *testing.T, h *hungPreview) {
				execSQL(t, h.hungBuild, "UPDATE memberships SET role='viewer' WHERE workspace_id=$1", h.ws)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungPreview(t)
			entered, release := h.provider.hold(t, "/pulls/7")
			h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
			waitEntered(t, entered)
			h.requireNothingHeld(t, uuid.Nil)
			tc.during(t, h)
			release()
			deadline := time.Now().Add(20 * time.Second)
			for {
				var unfinished, finished int
				if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FILTER(WHERE finalized_at IS NULL),count(*) FILTER(WHERE finalized_at IS NOT NULL) FROM river_job WHERE kind=$1 AND (args->>'number')::int=7", "platform_preview_v1").Scan(&unfinished, &finished); err != nil {
					t.Fatal(err)
				}
				if unfinished == 0 && finished > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the preview job never finished", unfinished, finished)
				}
				time.Sleep(30 * time.Millisecond)
			}
			var requests int
			if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM preview_requests WHERE workspace_id=$1", h.ws).Scan(&requests); err != nil {
				t.Fatal(err)
			}
			if requests != tc.requests {
				t.Fatal("preview requests", requests, "want", tc.requests)
			}
		})
	}
}

// A job holding an older answer must not overwrite what another job has recorded since, which
// would cancel a newer build and start an older one, or reopen a closed request. The other job
// here is a real one, so this depends on its commit moving what the question is keyed on.
func TestPreviewWorkerAsksAgainAfterAnotherJobRecordsTheRequest(t *testing.T) {
	newer := strings.Repeat("d", 40)
	for _, tc := range []struct {
		name   string
		change func(h *hungPreview)
		check  string
		want   string
	}{
		{
			name:   "head moved",
			change: func(h *hungPreview) { h.provider.pullHead.Store(newer) },
			check:  "SELECT head FROM preview_requests WHERE id=$1",
			want:   newer,
		},
		{
			name:   "request closed",
			change: func(h *hungPreview) { h.provider.pullClosed.Store(true) },
			check:  "SELECT state FROM preview_requests WHERE id=$1",
			want:   "deleted",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungPreview(t)
			h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
			preview := h.waitPreview(t, "approval_required")
			read := func() string {
				t.Helper()
				var got string
				if err := h.f.st.Pool.QueryRow(t.Context(), tc.check, preview.Id).Scan(&got); err != nil {
					t.Fatal(err)
				}
				return got
			}
			entered, release := h.provider.holdOnce(t, "/pulls/7")
			h.pullRequestHook(t)
			waitEntered(t, entered)
			tc.change(h)
			h.pullRequestHook(t)
			deadline := time.Now().Add(15 * time.Second)
			for read() != tc.want {
				if time.Now().After(deadline) {
					t.Fatal("the second job never recorded the change")
				}
				time.Sleep(30 * time.Millisecond)
			}
			release()
			for {
				var unfinished int
				if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind='platform_preview_v1' AND finalized_at IS NULL").Scan(&unfinished); err != nil {
					t.Fatal(err)
				}
				if unfinished == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the held job never finished")
				}
				time.Sleep(30 * time.Millisecond)
			}
			if got := read(); got != tc.want {
				t.Fatal("an older answer overwrote what another job recorded", got, "want", tc.want)
			}
		})
	}
}
