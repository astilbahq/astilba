package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
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
	var enrollment api.Enrollment
	h.b.call("POST", h.prefix+"/servers", map[string]any{"name": "Preview host", "role": "combined", "previewOnly": true}, 201, &enrollment)
	credential, _ := cryptobox.Token()
	yes := true
	input := api.AgentEnrollment{ServerId: enrollment.Server.Id, Token: enrollment.Token, Credential: credential, MachineId: uuid.New(), Capabilities: api.AgentCapabilities{Protocol: 1, Version: "preview-fixture", Os: "linux", Arch: "arm64", Docker: true, Build: &yes, BuildControls: &yes, BuildCache: &yes, Visual: &yes, Previews: &yes, ServiceOutputs: &yes, SafeReleases: &yes, CredentialFreeVisualCancel: &yes}, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 8 << 30, Notes: []string{}}}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	h.server, h.enrolled, h.token = enrollment.Server.Id, input, enrollment.Server.Id.String()+"."+credential
	var err error
	if h.identity, err = h.f.execution.Authenticate(t.Context(), h.server, credential); err != nil {
		t.Fatal(err)
	}
	build := pipeline.BuildConfig{Builder: "dockerfile", ConnectionID: h.connection.ID, Branch: "main", RunnerID: h.server, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, Budget: &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 512, Minutes: 5}}
	return &hungPreview{h, map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "build": build, "maxActive": 1, "maxConcurrentBuilds": 1, "limits": map[string]int{"cpus": 1, "memoryMiB": 256, "pids": 64}, "closeGraceHours": 24, "generatedDomains": false}}
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
			entered, release := h.provider.hold(t, "/branches/")
			finished := h.b.send("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy})
			waitEntered(t, entered)
			h.requireNothingHeld(t, uuid.Nil)
			// The reported failure: a poll behind the held row gave up after five seconds.
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			started := time.Now()
			if _, err := h.f.execution.Poll(ctx, h.identity); !errors.Is(err, context.DeadlineExceeded) {
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
	entered, release := h.provider.hold(t, "/branches/")
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
	entered, release := h.provider.hold(t, "/branches/")
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
