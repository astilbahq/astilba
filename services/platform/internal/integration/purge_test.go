package integration

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// secretEnvironment creates an environment like environment does, whose service also reads a
// secret, and returns the secret's version beside it.
func (x retirementFixture) secretEnvironment(name, domainName string) (api.Environment, string, api.Revision, api.SecretVersion) {
	x.t.Helper()
	env, path, draft := x.environment(name, domainName)
	var secret api.SecretVersion
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "Token", "value": "dummy-purge-canary"}, 201, &secret)
	_, spec := draftSpec(x.t, x.owner, path)
	spec.Applications[0].Variables = []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}}
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	return env, path, draft, secret
}

func (x retirementFixture) purge(b *browser, path, name string, volumes []uuid.UUID, status int) api.Environment {
	x.t.Helper()
	var out api.Environment
	raw := b.call("POST", path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: name, VolumeIds: volumes}, status, nil)
	if status == 201 {
		if err := json.Unmarshal(raw, &out); err != nil {
			x.t.Fatal(err)
		}
	}
	return out
}

func purgeCommand(t *testing.T, c api.ExecutionCommand) deployment.Payload {
	t.Helper()
	var p deployment.Payload
	if c.Kind != "visual" || c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Action != "purge" {
		t.Fatalf("expected a purge command, got %s", c.Kind)
	}
	return p
}

// Purging deletes a retired environment's retained volumes through its server and erases its secret
// values at once. Only owners who confirmed recently, naming the environment and exactly the volumes
// it keeps, may ask, and a purged environment is never restored.
func TestPurgeDeletesRetainedVolumesAndErasesSecrets(t *testing.T) {
	x := newRetirementFixture(t)
	env, path, draft, secret := x.secretEnvironment("Staging", "purge.example.test")
	x.deploy(path, draft)
	_, spec := draftSpec(t, x.owner, path)
	volumes := []uuid.UUID{spec.Volumes[0].ID}

	// Only a retired environment can be purged.
	x.refusal(x.owner, "POST", path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Staging", VolumeIds: volumes}, "retirement."+domain.RetirementState)
	x.lifecycle(x.owner, path, "retire", "Staging", 201)
	x.report(x.agent.next(), "succeeded", "Removed.")

	// Who may ask, and what they must name.
	x.owner.call("POST", "/api/v1/workspaces/"+x.ws.String()+"/invitations", map[string]string{"email": "operator@example.test", "role": "operator"}, 201, nil)
	operator := x.f.browser(t)
	var accepted api.Session
	operator.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, x.f, "/invitation"), "name": "Operator", "password": x.f.password}, 200, &accepted)
	operator.csrf = accepted.CsrfToken
	x.purge(operator, path, "Staging", volumes, 403)
	x.f.auth.Now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	x.purge(x.owner, path, "Staging", volumes, 403)
	x.f.auth.Now = time.Now
	x.purge(x.owner, path, "staging", volumes, 400)
	x.refusal(x.owner, "POST", path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Staging", VolumeIds: []uuid.UUID{}}, "retirement."+domain.RetirementVolumes)
	x.refusal(x.owner, "POST", path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Staging", VolumeIds: append(slices.Clone(volumes), uuid.New())}, "retirement."+domain.RetirementVolumes)

	// An agent that cannot purge is not sent a command it would refuse.
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/heartbeat", x.agent.token, api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Protocol: 1, Version: "test", Os: "linux", Arch: "arm64", Docker: true, Visual: x.agent.in.Capabilities.Visual, Retirement: x.agent.in.Capabilities.Retirement}, Preflight: x.agent.in.Preflight}, 200, nil)
	x.refusal(x.owner, "POST", path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Staging", VolumeIds: volumes}, "retirement."+domain.RetirementPurgeAgent)
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/heartbeat", x.agent.token, api.AgentHeartbeat{Capabilities: x.agent.in.Capabilities, Preflight: x.agent.in.Preflight}, 200, nil)
	if n := x.count("SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND erased_at IS NOT NULL", x.ws); n != 0 {
		t.Fatalf("a refused purge erased %d secret values", n)
	}

	purging := x.purge(x.owner, path, "Staging", volumes, 201)
	if purging.State != api.EnvironmentStatePurging || purging.PurgeOperationId == nil || purging.PurgedAt == nil || purging.PurgedBy == nil {
		t.Fatalf("purging environment: %+v", purging)
	}
	// The secret values are gone at once; the version rows stay for the revisions that name them.
	if n := x.count("SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NULL AND erased_at IS NOT NULL", x.ws, secret.Id); n != 1 {
		t.Fatal("the purge kept the secret value")
	}
	var listed []api.SecretVersion
	x.owner.call("GET", path+"/secrets", nil, 200, &listed)
	if len(listed) != 0 {
		t.Fatalf("erased secrets are listed: %+v", listed)
	}
	x.refusal(x.owner, "POST", path+"/restore", api.EnvironmentLifecycleRequest{ConfirmName: "Staging"}, "retirement."+domain.RetirementState)
	x.refusal(x.owner, "POST", path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Staging"}, "retirement."+domain.RetirementState)
	x.refusal(x.owner, "POST", path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Staging", VolumeIds: volumes}, "retirement."+domain.RetirementActiveOperation)
	x.refusal(x.owner, "POST", path+"/secrets", map[string]string{"name": "Other", "value": "new"}, "environment.state")

	c := x.agent.next()
	payload := purgeCommand(t, c)
	if c.Id != *purging.PurgeOperationId || c.ServerId != x.server || c.EnvironmentId != env.Id || c.ResourceId != env.DeploymentUnit.Id || !slices.Equal(payload.VolumeIDs, volumes) || len(payload.Secrets) != 0 || len(payload.RegistryAccess) != 0 || len(payload.Plan.Specification.Applications) != 0 {
		t.Fatalf("purge command does not name the retained volumes alone: %+v %+v", c, payload)
	}
	// A host that could not finish leaves it purging, and asking again queues a new command.
	x.report(c, "failed", "Ownership of a retained volume could not be verified.")
	var still api.Environment
	x.owner.call("GET", path, nil, 200, &still)
	if still.State != api.EnvironmentStatePurging {
		t.Fatalf("a failed purge changed the environment: %+v", still)
	}
	if n := x.count("SELECT count(*) FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND NOT deleted", x.ws, env.Id); n != 1 {
		t.Fatal("a failed purge marked its volume deleted")
	}
	again := x.purge(x.owner, path, "Staging", volumes, 201)
	if again.PurgeOperationId == nil || *again.PurgeOperationId == c.Id {
		t.Fatal("asking again did not queue a new purge")
	}
	retried := x.agent.next()
	purgeCommand(t, retried)
	if retried.Id != *again.PurgeOperationId || retried.Generation <= c.Generation {
		t.Fatal("the new purge was not fenced above the failed one")
	}
	x.report(retried, "succeeded", "The retained volumes were deleted.")
	var purged api.Environment
	x.owner.call("GET", path, nil, 200, &purged)
	if purged.State != api.EnvironmentStatePurged || purged.PurgedWithoutHost == nil || *purged.PurgedWithoutHost {
		t.Fatalf("purged environment: %+v", purged)
	}
	var kept []api.ManagedVolume
	x.owner.call("GET", path+"/volumes", nil, 200, &kept)
	if len(kept) != 1 || !kept[0].Deleted {
		t.Fatalf("volumes after the purge: %+v", kept)
	}
	x.refusal(x.owner, "POST", path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Staging", VolumeIds: []uuid.UUID{}}, "retirement."+domain.RetirementState)
	// Its history and last deployed configuration stay readable.
	x.owner.call("GET", path+"/revisions", nil, 200, nil)
	x.owner.call("GET", path+"/export", nil, 200, nil)
	if n := x.count("SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='environment.purge' AND resource_id=$2", x.ws, env.Id); n != 2 {
		t.Fatalf("purge audit events: %d", n)
	}
}

// An environment that kept no volumes is purged at once, and one whose server was revoked finishes
// without it and says so: its volumes stay listed, because nothing deleted them.
func TestPurgeWithoutHost(t *testing.T) {
	x := newRetirementFixture(t)
	var fresh api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Draft only"}, 201, &fresh)
	freshPath := x.prefix + "/" + fresh.Id.String()
	x.lifecycle(x.owner, freshPath, "retire", "Draft only", 201)
	if v := x.purge(x.owner, freshPath, "Draft only", []uuid.UUID{}, 201); v.State != api.EnvironmentStatePurged || v.PurgeOperationId != nil || *v.PurgedWithoutHost {
		t.Fatalf("an environment without volumes: %+v", v)
	}

	env, path, draft, secret := x.secretEnvironment("Legacy", "unreached-purge.example.test")
	x.deploy(path, draft)
	_, spec := draftSpec(t, x.owner, path)
	x.owner.call("POST", "/api/v1/workspaces/"+x.ws.String()+"/servers/"+x.server.String()+"/revoke", nil, 204, nil)
	x.lifecycle(x.owner, path, "retire", "Legacy", 201)
	v := x.purge(x.owner, path, "Legacy", []uuid.UUID{spec.Volumes[0].ID}, 201)
	if v.State != api.EnvironmentStatePurged || v.PurgeOperationId != nil || v.PurgedWithoutHost == nil || !*v.PurgedWithoutHost {
		t.Fatalf("purged without its host: %+v", v)
	}
	if n := x.count("SELECT count(*) FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND NOT deleted", x.ws, env.Id); n != 1 {
		t.Fatal("a volume no server deleted was marked deleted")
	}
	if n := x.count("SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND erased_at IS NOT NULL", x.ws, secret.Id); n != 1 {
		t.Fatal("purging without the host kept the secret value")
	}
}

// Retiring one secret erases every version's value and frees its name, but only once nothing that
// may still deploy names one of its versions: the draft, the running deployment, a plan or an
// operation.
func TestRetireSecretOnlyWhenUnused(t *testing.T) {
	x := newRetirementFixture(t)
	_, path, draft, used := x.secretEnvironment("Production", "secrets.example.test")
	x.deploy(path, draft)
	var spare api.SecretVersion
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "Spare", "value": "dummy-spare-one"}, 201, &spare)
	x.owner.call("POST", path+"/secrets/"+spare.SecretId.String()+"/versions", map[string]string{"value": "dummy-spare-two"}, 201, nil)

	// Who may ask.
	x.owner.call("POST", "/api/v1/workspaces/"+x.ws.String()+"/invitations", map[string]string{"email": "operator@example.test", "role": "operator"}, 201, nil)
	operator := x.f.browser(t)
	var accepted api.Session
	operator.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, x.f, "/invitation"), "name": "Operator", "password": x.f.password}, 200, &accepted)
	operator.csrf = accepted.CsrfToken
	operator.call("DELETE", path+"/secrets/"+spare.SecretId.String(), nil, 403, nil)
	x.f.auth.Now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	x.owner.call("DELETE", path+"/secrets/"+spare.SecretId.String(), nil, 403, nil)
	x.f.auth.Now = time.Now

	// The draft and the running deployment name the used secret.
	x.refusal(x.owner, "DELETE", path+"/secrets/"+used.SecretId.String(), nil, "secret.inUse")
	_, spec := draftSpec(t, x.owner, path)
	spec.Applications[0].Variables = []domain.Variable{}
	var edited api.Revision
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &edited)
	x.refusal(x.owner, "DELETE", path+"/secrets/"+used.SecretId.String(), nil, "secret.inUse")

	// A plan that can still be applied names the spare secret once the draft does.
	spec.Applications[0].Variables = []domain.Variable{{Name: "SPARE", SecretVersionID: &spare.Id}}
	var planned api.Revision
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": edited.Id, "specification": spec}, 200, &planned)
	var plan deployment.Status
	x.owner.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: x.server, BaseRevisionId: planned.Id}, 201, &plan)
	spec.Applications[0].Variables = []domain.Variable{}
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": planned.Id, "specification": spec}, 200, &edited)
	x.refusal(x.owner, "DELETE", path+"/secrets/"+spare.SecretId.String(), nil, "secret.inUse")
	x.report(x.agent.next(), "failed", "Resolution stopped.")

	// Nothing that can still deploy names it now.
	x.owner.call("DELETE", path+"/secrets/"+spare.SecretId.String(), nil, 204, nil)
	if n := x.count("SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND secret_id=$2 AND encrypted_value IS NULL AND erased_at IS NOT NULL", x.ws, spare.SecretId); n != 2 {
		t.Fatalf("erased versions: %d", n)
	}
	var listed []api.SecretVersion
	x.owner.call("GET", path+"/secrets", nil, 200, &listed)
	for _, v := range listed {
		if v.SecretId == spare.SecretId {
			t.Fatal("a retired secret is listed")
		}
	}
	x.owner.call("POST", path+"/secrets/"+spare.SecretId.String()+"/versions", map[string]string{"value": "dummy-spare-three"}, 404, nil)
	x.owner.call("DELETE", path+"/secrets/"+spare.SecretId.String(), nil, 404, nil)
	// Its name can be used again, and a draft cannot name an erased version.
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "Spare", "value": "dummy-spare-four"}, 201, nil)
	spec.Applications[0].Variables = []domain.Variable{{Name: "SPARE", SecretVersionID: &spare.Id}}
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": edited.Id, "specification": spec}, 400, nil)
	if n := x.count("SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='secret.retire' AND resource_id=$2", x.ws, spare.SecretId); n != 1 {
		t.Fatalf("secret retirement audit events: %d", n)
	}
}

// A preview policy creates previews from the template revision it was saved with, which can name a
// secret the template's draft no longer does, so that secret stays in use.
func TestRetireSecretKeepsAPreviewPolicysSecret(t *testing.T) {
	h := newHungPreview(t)
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "Token", "value": "dummy-preview-canary"}, 201, &secret)
	spec := h.spec
	spec.Applications = slices.Clone(spec.Applications)
	spec.Applications[0].Variables = []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}}
	var pinned api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &pinned)
	policy := maps.Clone(h.policy)
	policy["baseRevisionId"] = pinned.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": policy}, 200, nil)
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": pinned.Id, "specification": h.spec}, 200, nil)
	var failure api.Error
	if err := json.Unmarshal(h.b.call("DELETE", h.path+"/secrets/"+secret.SecretId.String(), nil, 409, nil), &failure); err != nil || failure.Fields == nil || !slices.Contains(*failure.Fields, "secret.inUse") {
		t.Fatalf("a secret the preview policy's revision names was retired: %+v %v", failure, err)
	}
}

// A workspace export carries a purge and its erased secret versions without values, so an imported
// purged environment stays purged and its secrets stay erased.
func TestPurgedEnvironmentSurvivesExportAndImport(t *testing.T) {
	x := newRetirementFixture(t)
	ctx := context.Background()
	env, path, draft, secret := x.secretEnvironment("Staging", "exported-purge.example.test")
	x.deploy(path, draft)
	_, spec := draftSpec(t, x.owner, path)
	x.lifecycle(x.owner, path, "retire", "Staging", 201)
	x.report(x.agent.next(), "succeeded", "Removed.")
	purging := x.purge(x.owner, path, "Staging", []uuid.UUID{spec.Volumes[0].ID}, 201)
	x.report(x.agent.next(), "succeeded", "Deleted.")
	// The environment names its purge operation, so archives carry it however much newer history
	// pushes it out of the recent operations they keep.
	if _, err := x.f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,created_at)
		SELECT gen_random_uuid(),$1,$2,0,gen_random_uuid(),'{}'::jsonb,'test','succeeded',now()+n*interval '1 second' FROM generate_series(1,101) n`, x.ws, x.server); err != nil {
		t.Fatal(err)
	}
	tx, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base, err := portability.CaptureRecovery(ctx, tx, x.ws)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = portability.DecodeRecovery(base); err != nil {
		t.Fatal("a recovery base without its purge operation", err)
	}

	account, err := x.f.st.Queries.AccountByID(ctx, x.f.owner)
	if err != nil {
		t.Fatal(err)
	}
	const passphrase = "purged environment transfer passphrase"
	archive, err := (&portability.Service{Store: x.f.st}).Export(ctx, browserPrincipal(t, x.f.st, account, true), x.ws, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	destination := setup(t)
	importer, err := destination.st.Queries.AccountByID(ctx, destination.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&portability.Service{Store: destination.st}).Import(ctx, browserPrincipal(t, destination.st, importer, true), passphrase, archive, ""); err != nil {
		t.Fatal(err)
	}
	var state string
	var operation, by *uuid.UUID
	if err = destination.st.Pool.QueryRow(ctx, "SELECT state,purge_operation_id,purged_by FROM environments WHERE workspace_id=$1 AND id=$2", x.ws, env.Id).Scan(&state, &operation, &by); err != nil {
		t.Fatal(err)
	}
	if state != "purged" || operation == nil || *operation != *purging.PurgeOperationId || by == nil || *by != importer.ID {
		t.Fatalf("imported purge: %s, operation %v, by %v", state, operation, by)
	}
	var erased int
	if err = destination.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NULL AND erased_at IS NOT NULL", x.ws, secret.Id).Scan(&erased); err != nil || erased != 1 {
		t.Fatalf("imported erased versions: %d %v", erased, err)
	}
}

// previewSecretTemplate turns on h's preview policy for a template revision whose service reads a
// secret, so each preview holds its own copy of the secret's value.
func previewSecretTemplate(t *testing.T, h *hungPreview) api.SecretVersion {
	t.Helper()
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "Token", "value": "dummy-template-canary"}, 201, &secret)
	spec := h.spec
	spec.Applications = slices.Clone(spec.Applications)
	spec.Applications[0].Variables = []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}}
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &h.draft)
	policy := maps.Clone(h.policy)
	policy["baseRevisionId"] = h.draft.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": policy}, 200, nil)
	return secret
}

// environmentSecretValues counts the secret versions of env that still hold a value.
func environmentSecretValues(t *testing.T, h *hungPreview, env uuid.UUID) int {
	t.Helper()
	var n int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM secret_versions v JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id WHERE s.workspace_id=$1 AND s.environment_id=$2 AND v.encrypted_value IS NOT NULL", h.ws, env).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func previewsRefusal(t *testing.T, raw []byte) api.Error {
	t.Helper()
	var refused api.Error
	if err := json.Unmarshal(raw, &refused); err != nil || refused.Fields == nil || !slices.Contains(*refused.Fields, "retirement.previews") {
		t.Fatalf("expected the previews refusal: %s %v", raw, err)
	}
	return refused
}

// retiredTemplateWithoutPreviews retires h's template once its ready preview's cleanup has deleted
// it, and returns that preview.
func retiredTemplateWithoutPreviews(t *testing.T, h *hungPreview) api.PullRequestPreview {
	t.Helper()
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := readyPreview(t, h)
	h.b.call("POST", h.path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "PR template"}, 201, nil)
	waitPreviewAt(t, h, h.path, "deleting")
	cleanup := nextPreviewCommand(t, h)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: cleanup.Id, Generation: cleanup.Generation, PlanHash: cleanup.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)
	waitPreviewAt(t, h, h.path, "deleted")
	waitPreviewChecks(t, h)
	return preview
}

// A preview template's previews hold their own copies of its secret values until their cleanup
// deletes them, so purging the template is refused, naming each preview and what it waits for, until
// every one is deleted. A purge that succeeds leaves no copy.
func TestPurgeWaitsForATemplatesPreviews(t *testing.T) {
	h := newHungPreview(t)
	var environment api.Environment
	h.b.call("GET", h.path, nil, 200, &environment)
	template := environment.Id
	secret := previewSecretTemplate(t, h)
	preview := readyPreview(t, h)
	if environmentSecretValues(t, h, *preview.EnvironmentId) != 1 {
		t.Fatal("the preview holds no copy of the template's secret")
	}
	h.b.call("POST", h.path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "PR template"}, 201, nil)
	deleting := waitPreviewAt(t, h, h.path, "deleting")
	cleanup := nextPreviewCommand(t, h)

	purge := func(status int) []byte {
		t.Helper()
		return h.b.call("POST", h.path+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "PR template", VolumeIds: []uuid.UUID{}}, status, nil)
	}
	refused := previewsRefusal(t, purge(409))
	if want := fmt.Sprintf("#%d is deleting (%s)", deleting.Number, strings.TrimSuffix(deleting.Note, ".")); !strings.Contains(refused.Message, want) {
		t.Fatalf("the refusal does not name the preview and why it waits, %q: %s", want, refused.Message)
	}
	if environmentSecretValues(t, h, template) != 1 {
		t.Fatal("a refused purge erased the template's secret")
	}

	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: cleanup.Id, Generation: cleanup.Generation, PlanHash: cleanup.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)
	waitPreviewAt(t, h, h.path, "deleted")
	// A request never given an environment holds no copy, so it does not hold the purge back, even
	// one blocked for good because its host was revoked.
	if _, err := h.f.st.Pool.Exec(t.Context(), "INSERT INTO preview_requests(id,workspace_id,policy_id,number,state,note) SELECT $1,workspace_id,policy_id,77,'blocked','Cleanup needs renewed owner approval or a verified agent transfer.' FROM preview_requests WHERE workspace_id=$2 AND id=$3", uuid.New(), h.ws, preview.Id); err != nil {
		t.Fatal(err)
	}
	var purged api.Environment
	if err := json.Unmarshal(purge(201), &purged); err != nil || purged.State != api.EnvironmentStatePurged {
		t.Fatalf("purged template: %+v %v", purged, err)
	}
	if environmentSecretValues(t, h, template) != 0 || environmentSecretValues(t, h, *preview.EnvironmentId) != 0 {
		t.Fatal("a purged template's secret value remains in it or its preview")
	}
	var erased bool
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT erased_at IS NOT NULL FROM secret_versions WHERE workspace_id=$1 AND id=$2", h.ws, secret.Id).Scan(&erased); err != nil || !erased {
		t.Fatal("the template's secret version was not erased", err)
	}
}

// A purge checks the template's previews under the workspace write lock that every change to a
// preview holds. One that waits for that lock sees what the holder committed, so a preview that
// appears or comes back while it waits refuses it.
func TestTemplatePurgeSeesAPreviewChangedWhileItWaits(t *testing.T) {
	h := newHungPreview(t)
	ctx := t.Context()
	preview := retiredTemplateWithoutPreviews(t, h)

	// This transaction stands in for any change to a preview: it holds the workspace write lock and
	// brings the preview back.
	tx, err := h.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var holder int32
	if err = tx.QueryRow(ctx, "SELECT pg_backend_pid() FROM workspaces WHERE id=$1 FOR NO KEY UPDATE", h.ws).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE preview_requests SET state='blocked',note='Brought back while the purge waited.' WHERE workspace_id=$1 AND id=$2", h.ws, preview.Id); err != nil {
		t.Fatal(err)
	}
	waiting := func() int {
		t.Helper()
		var n int
		if err := h.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))", holder).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := waiting()
	type answer struct {
		status int
		body   []byte
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		raw, _ := stablejson.Marshal(api.EnvironmentPurgeRequest{ConfirmName: "PR template", VolumeIds: []uuid.UUID{}})
		request, err := http.NewRequestWithContext(ctx, "POST", h.f.server.URL+h.path+"/purge", bytes.NewReader(raw))
		if err != nil {
			answered <- answer{err: err}
			return
		}
		request.Header.Set("Origin", h.f.server.URL)
		request.Header.Set("X-CSRF-Token", h.b.csrf)
		request.Header.Set("Content-Type", "application/json")
		response, err := h.b.client.Do(request)
		if err != nil {
			answered <- answer{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		answered <- answer{response.StatusCode, body, err}
	}()
	for deadline := time.Now().Add(10 * time.Second); waiting() <= before; time.Sleep(10 * time.Millisecond) {
		select {
		case a := <-answered:
			t.Fatalf("the purge answered without waiting for the workspace lock: %d %s %v", a.status, a.body, a.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the purge never waited for the workspace lock")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	a := <-answered
	if a.err != nil || a.status != 409 {
		t.Fatalf("purge after the preview came back: %d %s %v", a.status, a.body, a.err)
	}
	if refused := previewsRefusal(t, a.body); !strings.Contains(refused.Message, fmt.Sprintf("#%d is blocked (Brought back while the purge waited)", preview.Number)) {
		t.Fatalf("the refusal does not name the preview that came back: %s", refused.Message)
	}
	var template api.Environment
	h.b.call("GET", h.path, nil, 200, &template)
	if template.State != api.EnvironmentStateRetired {
		t.Fatalf("a refused purge changed the template: %s", template.State)
	}
}
