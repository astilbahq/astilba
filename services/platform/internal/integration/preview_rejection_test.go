package integration

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

// A saved policy can fail a stricter check than the one it passed, and a reconcile that
// returns the rejection rolls back its reason and retries the provider for days. Later passes
// neither create the environment again nor copy its secrets; once the policy is corrected, the
// preview builds.
func TestPreviewBuildRejectionSettlesTheRequest(t *testing.T) {
	h := newHungPreview(t)
	const canary = "preview-pasted-canary"
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "TOKEN", "value": canary + "\n"}, 201, &secret)
	spec := h.spec
	spec.Variables = append(append([]domain.Variable{}, spec.Variables...), domain.Variable{Name: "TOKEN", SecretVersionID: &secret.Id})
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id
	var policy api.PreviewPolicy
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, &policy)
	environments := countInserts(t, h, "environments")
	if _, err := h.f.st.Pool.Exec(t.Context(), "UPDATE preview_policies SET configuration=jsonb_set(configuration,'{build,arguments}',jsonb_build_object('TOKEN',$1::text)) WHERE workspace_id=$2", canary, h.ws); err != nil {
		t.Fatal(err)
	}
	preview := h.waitPreview(t, "approval_required")
	h.b.call("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	preview = h.waitPreview(t, "failed")
	if !strings.Contains(preview.Note, "known secret") || strings.Contains(preview.Note, canary) {
		t.Fatal("the rejection did not reach the preview, or echoed the value", preview.Note)
	}
	if preview.EnvironmentId != nil {
		t.Fatal("a request that can never build kept an environment and its resource slot")
	}
	if !strings.HasPrefix(preview.Note, "Preview build rejected before runner delivery: ") || !strings.HasSuffix(preview.Note, creationRetry) {
		t.Fatalf("the preview's note is %q", preview.Note)
	}
	stillRefused(t, h, preview, []func() int64{environments}, true)
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": policy.Generation, "configuration": h.policy}, 200, nil)
	if preview = approvePreview(t, h, h.path); preview.EnvironmentId == nil {
		t.Fatal("the corrected policy's preview has no environment")
	}
}

// A preview build can be admitted while one of its environment's secrets is out of the check's
// reach, and find that secret in its arguments at delivery. The build would be refused again at
// every poll until its deadline, so the first poll ends it, and later polls neither deliver it nor
// search the secrets again.
func TestPreviewBuildRejectedAtDeliverySettlesOnce(t *testing.T) {
	h := newHungPreview(t)
	const canary = "preview-delivery-canary"
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, &secret)
	spec := h.spec
	spec.Variables = append(append([]domain.Variable{}, spec.Variables...), domain.Variable{Name: "TOKEN", SecretVersionID: &secret.Id})
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := approvePreview(t, h, h.path)
	first := nextPreviewCommand(t, h)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: first.Id, Generation: first.Generation, PlanHash: first.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	h.waitPreview(t, "failed")
	// The next commit's build is admitted while the environment's copy of the secret is retired,
	// and its arguments come to hold the secret's value. The copy is back before the runner polls.
	execSQL(t, h.hungBuild, "UPDATE secrets SET retired_at=now() WHERE environment_id=$1", *preview.EnvironmentId)
	execSQL(t, h.hungBuild, "UPDATE preview_requests SET configuration=jsonb_set(configuration,'{build,arguments}',jsonb_build_object('TOKEN',$1::text)) WHERE id=$2", canary, preview.Id)
	pushPreviewCommit(t, h, strings.Repeat("b", 40))
	preview = h.waitPreview(t, "building")
	operation := *preview.OperationId
	if operation == first.Id {
		t.Fatal("the new commit's build was not admitted")
	}
	h.waitState(t, operation, "dispatched")
	execSQL(t, h.hungBuild, "UPDATE secrets SET retired_at=NULL WHERE environment_id=$1", *preview.EnvironmentId)
	var polled api.AgentPoll
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", h.token, nil, 200, &polled)
	if polled.Command != nil {
		t.Fatal("the rejected build was delivered", polled.Command.Id)
	}
	var state, note string
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note FROM execution_operations WHERE id=$1", operation).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !strings.HasSuffix(note, "Build rejected before runner delivery: Public build arguments contain a known secret value.") || strings.Contains(note, canary) {
		t.Fatalf("the build ended %s with the note %q", state, note)
	}
	h.waitPreview(t, "failed")
	searched := secretAccesses(t, h)
	for range 3 {
		agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", h.token, nil, 200, &polled)
		if polled.Command != nil {
			t.Fatal("a later poll delivered", polled.Command.Id)
		}
	}
	if secretAccesses(t, h) != searched {
		t.Fatal("a later poll checked the settled build's inputs again")
	}
}

// A preview build its runner may hold has its values released again when the runner asks for
// them, and the check then finds a secret in its arguments that was out of its reach at the first
// delivery. Refusing that poll would refuse every later one until the build's deadline, and hold
// the runner's other commands, so the runner is told to stop the build, without its values, and
// its later commands reach it.
func TestPreviewBuildRejectedAtRedeliveryStops(t *testing.T) {
	h := newHungPreview(t)
	const canary = "preview-redelivery-canary"
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, &secret)
	spec := h.spec
	spec.Variables = append(append([]domain.Variable{}, spec.Variables...), domain.Variable{Name: "TOKEN", SecretVersionID: &secret.Id})
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := approvePreview(t, h, h.path)
	first := nextPreviewCommand(t, h)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: first.Id, Generation: first.Generation, PlanHash: first.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	h.waitPreview(t, "failed")
	// The next commit's build is admitted and delivered while the environment's copy of the
	// secret is retired, and its arguments hold the secret's value.
	execSQL(t, h.hungBuild, "UPDATE secrets SET retired_at=now() WHERE environment_id=$1", *preview.EnvironmentId)
	execSQL(t, h.hungBuild, "UPDATE preview_requests SET configuration=jsonb_set(configuration,'{build,arguments}',jsonb_build_object('TOKEN',$1::text)) WHERE id=$2", canary, preview.Id)
	pushPreviewCommit(t, h, strings.Repeat("b", 40))
	preview = h.waitPreview(t, "building")
	operation := *preview.OperationId
	h.waitState(t, operation, "dispatched")
	delivered := nextPreviewCommand(t, h)
	if delivered.Id != operation || delivered.CancelRequested || delivered.SecretRelease == nil || !delivered.SecretRelease.Values {
		t.Fatal("the build was not delivered with its values", delivered.Id, delivered.CancelRequested)
	}
	// The copy is back when the runner, which has lost the build's values, asks for them again.
	execSQL(t, h.hungBuild, "UPDATE secrets SET retired_at=NULL WHERE environment_id=$1", *preview.EnvironmentId)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/releases", h.token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: operation, Epoch: delivered.SecretRelease.Epoch, Reason: api.JournalMissing}}}, 204, nil)
	stop := nextPreviewCommand(t, h)
	var command pipeline.Command
	if stop.Id != operation || !stop.CancelRequested || stop.SecretRelease != nil || json.Unmarshal(*stop.Pipeline, &command) != nil || command.Access != nil || command.BuildSecrets != nil {
		t.Fatal("the rejected build was not stopped without its values", stop.Id, stop.CancelRequested, stop.SecretRelease)
	}
	var state, note string
	var cancelled bool
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note,cancel_requested FROM execution_operations WHERE id=$1", operation).Scan(&state, &note, &cancelled); err != nil {
		t.Fatal(err)
	}
	// It ends when the runner reports it, since the runner may be running it.
	if state != "dispatched" || !cancelled || note != "Build stopped: Public build arguments contain a known secret value." {
		t.Fatalf("the stopped build is %s, cancel requested %v, with the note %q", state, cancelled, note)
	}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: stop.Id, Generation: stop.Generation, PlanHash: stop.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	h.waitPreview(t, "failed")
	// The runner's next command reaches it.
	execSQL(t, h.hungBuild, "UPDATE preview_requests SET configuration=jsonb_set(configuration,'{build,arguments}','{}'::jsonb) WHERE id=$1", preview.Id)
	pushPreviewCommit(t, h, strings.Repeat("c", 40))
	preview = h.waitPreview(t, "building")
	if next := nextPreviewCommand(t, h); next.Id != *preview.OperationId || next.Id == operation || next.CancelRequested {
		t.Fatal("the stopped build held the runner's later commands", next.Id)
	}
}
