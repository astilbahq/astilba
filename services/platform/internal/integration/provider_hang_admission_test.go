package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func (h *hungPreview) waitPreview(t *testing.T, state string) api.PullRequestPreview {
	t.Helper()
	return waitPreviewAt(t, h, h.path, state)
}

// approvedPreviewBuild leaves an approved fork's build dispatched.
func approvedPreviewBuild(t *testing.T) (*hungPreview, api.PullRequestPreview) {
	t.Helper()
	h := newHungPreview(t)
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := h.waitPreview(t, "approval_required")
	h.b.call("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	preview = h.waitPreview(t, "building")
	h.waitState(t, *preview.OperationId, "dispatched")
	return h, preview
}

// The dispatcher cancels a command that stops being allowed, but only on its next run. A
// delivery that finishes before that run must find out for itself, because its clone
// credential and build secrets can't be recalled once they leave. These run the delivering
// pass straight after the change, ahead of the dispatcher. The poll's own provider check asks
// about the repository first, so the request held here is the delivery's.
func TestPreviewDeliveryRechecksAuthorityAfterAHungProvider(t *testing.T) {
	for _, tc := range []struct {
		name      string
		during    string
		delivered bool
	}{
		{name: "nothing changed", delivered: true},
		{name: "owner demoted", during: "UPDATE memberships SET role='viewer' WHERE workspace_id=(SELECT workspace_id FROM preview_requests WHERE id=$1)"},
		{name: "cleanup requested", during: "UPDATE preview_requests SET cleanup_requested=true WHERE id=$1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, preview := approvedPreviewBuild(t)
			operation := *preview.OperationId
			entered, release := h.provider.holdAfter(t, "/repos/team/app", 1)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			result := h.pollAsync(ctx)
			waitEntered(t, entered)
			if tc.during != "" {
				execSQL(t, h.hungBuild, tc.during, preview.Id)
			}
			release()
			if !tc.delivered {
				h.waitState(t, operation, "cancelled")
				cancel()
			}
			outcome := <-result
			if !tc.delivered {
				if carriesCredential(outcome.command) || h.deliveryStarted(t, operation) {
					t.Fatal("credentials were delivered after the command stopped being allowed", outcome.err)
				}
				return
			}
			if outcome.err != nil || !carriesCredential(outcome.command) || !h.deliveryStarted(t, operation) {
				t.Fatal("an unchanged preview delivery was lost", outcome.err)
			}
		})
	}
}

// A visual command hydrates straight after the provider check, and its secrets leave with it,
// so everything read before that check must be read again when it answers. This covers the
// operation and the agent as well as what the check was made against.
func TestPreviewVisualDeliveryRechecksAfterItsProviderCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during func(t *testing.T, h *hungPreview, operation, request uuid.UUID)
		err    error
	}{
		{
			name: "cleanup requested",
			during: func(t *testing.T, h *hungPreview, _, request uuid.UUID) {
				execSQL(t, h.hungBuild, "UPDATE preview_requests SET cleanup_requested=true WHERE id=$1", request)
			},
		},
		{
			name: "operation cancelled",
			during: func(t *testing.T, h *hungPreview, operation, _ uuid.UUID) {
				execSQL(t, h.hungBuild, "UPDATE execution_operations SET cancel_requested=true WHERE id=$1", operation)
			},
		},
		{
			name: "agent's credential replaced",
			during: func(t *testing.T, h *hungPreview, _, _ uuid.UUID) {
				execSQL(t, h.hungBuild, "UPDATE servers SET credential_hash=NULL WHERE id=$1", h.server)
			},
			err: domain.Unauthenticated,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, preview := approvedPreviewBuild(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			build, err := pollReleased(h.f.execution, ctx, h.identity)
			if err != nil || build == nil {
				t.Fatal("the preview build was not delivered", err)
			}
			if err = h.f.execution.Report(ctx, h.identity, h.succeeded(t, build)); err != nil {
				t.Fatal(err)
			}
			preview = h.waitPreview(t, "deploying")
			h.waitState(t, *preview.OperationId, "dispatched")
			entered, release := h.provider.hold(t, "/pulls/7")
			result := h.pollAsync(ctx)
			waitEntered(t, entered)
			tc.during(t, h, *preview.OperationId, preview.Id)
			release()
			outcome := <-result
			if !errors.Is(outcome.err, tc.err) {
				t.Fatal(outcome.err)
			}
			if tc.err == nil && (outcome.command == nil || !outcome.command.CancelRequested) {
				t.Fatal("a visual command was delivered without being cancelled after it stopped being allowed", outcome.command)
			}
		})
	}
}

// Poll asks the provider before delivering a preview command, with nothing open during the
// call, and a tenant's slow server must not pin a pool connection per polling runner.
func TestPreviewDeliveryHoldsNothingDuringItsProviderCheck(t *testing.T) {
	h, preview := approvedPreviewBuild(t)
	entered, release := h.provider.hold(t, "/pulls/7")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	result := h.pollAsync(ctx)
	waitEntered(t, entered)
	h.requireNothingHeld(t, *preview.OperationId)
	release()
	outcome := <-result
	if outcome.err != nil || !carriesCredential(outcome.command) {
		t.Fatal("an unchanged preview delivery was lost", outcome.err)
	}
}
