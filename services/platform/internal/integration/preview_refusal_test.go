package integration

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// refusalRetry ends the note of a preview step refused for good.
const refusalRetry = " It is tried again after a new commit, a change to the preview policy or an update of the preview host's agent, or when an owner refreshes the preview."

// countInserts counts the rows transactions try to insert into table from now on, including rows
// they roll back, as a refused step rolls back all it wrote. A sequence advances whatever happens
// to the transaction that advanced it.
func countInserts(t *testing.T, h *hungPreview, table string) func() int64 {
	t.Helper()
	name := table + "_attempts"
	execSQL(t, h.hungBuild, fmt.Sprintf(`CREATE SEQUENCE %[1]s;
		CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM nextval('%[1]s'); RETURN NEW; END$$;
		CREATE TRIGGER %[1]s BEFORE INSERT ON %[2]s FOR EACH ROW EXECUTE FUNCTION %[1]s()`, name, table))
	return func() int64 {
		t.Helper()
		var n int64
		var called bool
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT last_value,is_called FROM "+name).Scan(&n, &called); err != nil {
			t.Fatal(err)
		}
		if !called {
			return 0
		}
		return n
	}
}

// secretAccesses is how many times management has been given keys to copy or inspect the
// workspace's values, as creating a preview's environment copies its template's secrets and
// admitting a build searches its environment's. Every pass also asks the provider about the
// request, with keys to operate the connection.
func secretAccesses(t *testing.T, h *hungPreview) int {
	t.Helper()
	var n int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM secret_access WHERE workspace_id=$1 AND purpose IN ('copy','inspect')", h.ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// previewPass has the provider announce the request and waits for the pass that answers it.
func previewPass(t *testing.T, h *hungPreview, preview api.PullRequestPreview) api.PullRequestPreview {
	t.Helper()
	var before time.Time
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT updated_at FROM preview_requests WHERE id=$1", preview.Id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	h.pullRequestHook(t)
	deadline := time.Now().Add(20 * time.Second)
	for {
		var after time.Time
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT updated_at FROM preview_requests WHERE id=$1", preview.Id).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after.After(before) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no pass answered the request's event")
		}
		time.Sleep(30 * time.Millisecond)
	}
	var list []api.PullRequestPreview
	h.b.call("GET", h.path+"/previews", nil, 200, &list)
	return list[0]
}

// stillRefused checks that a pass after a refusal left the preview as the refusal did, and did none
// of the refused step's work: it inserted nothing into the tables counted and opened no value. opens
// is whether the refused step opened values.
func stillRefused(t *testing.T, h *hungPreview, refused api.PullRequestPreview, counted []func() int64, opens bool) {
	t.Helper()
	before := make([]int64, len(counted))
	for i, count := range counted {
		if before[i] = count(); before[i] == 0 {
			t.Fatal("the refused step did no work this test can see")
		}
	}
	opened := secretAccesses(t, h)
	if opens && opened == 0 {
		t.Fatal("the refused step opened no values")
	}
	again := previewPass(t, h, refused)
	if again.State != "failed" || again.Note != refused.Note || (again.EnvironmentId == nil) != (refused.EnvironmentId == nil) {
		t.Fatal("a later pass changed the refused preview", again.State, again.Note)
	}
	for i, count := range counted {
		if n := count(); n != before[i] {
			t.Fatalf("a later pass repeated the refused step: %d inserts, then %d", before[i], n)
		}
	}
	if n := secretAccesses(t, h); n != opened {
		t.Fatalf("a later pass opened values the refused step needs: %d accesses, then %d", opened, n)
	}
}

// reportPreviewBuild has the preview host run the build it is given and report its artifact.
func reportPreviewBuild(t *testing.T, h *hungPreview) {
	t.Helper()
	build := nextPreviewCommand(t, h)
	var command pipeline.Command
	if err := json.Unmarshal(*build.Pipeline, &command); err != nil {
		t.Fatal(err)
	}
	raw, _ := stablejson.Marshal(pipeline.Result{Commit: command.Commit, Image: "astilba-build-" + build.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("c", 64), Architecture: "arm64"})
	result := jsontext.Value(raw)
	report := api.AgentReport{Id: build.Id, Generation: build.Generation, PlanHash: build.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &result}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, report, 204, nil)
}

// pushPreviewCommit moves the request to a new fork commit and approves it.
func pushPreviewCommit(t *testing.T, h *hungPreview, head string) api.PullRequestPreview {
	t.Helper()
	h.provider.pullHead.Store(head)
	h.pullRequestHook(t)
	preview := h.waitPreview(t, "approval_required")
	if preview.Head != head {
		t.Fatal("the request did not move to the new commit", preview.Head)
	}
	h.b.call("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	return preview
}

// A preview host whose agent cannot run the template's database refuses the preview until the agent
// is updated, without copying the template's secrets again at every pass (PLAT-217).
func TestPreviewOnAHostThatNeedsAnAgentUpdateWaitsForTheUpdate(t *testing.T) {
	h := newHungPreview(t)
	current := h.enrolled.Capabilities
	outdated := current
	outdated.PostgresOwnerRole = nil
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/heartbeat", h.token, api.AgentHeartbeat{Capabilities: outdated, Preflight: h.enrolled.Preflight}, 200, nil)
	var password api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "DB_PASSWORD", "value": "template-password-canary"}, 201, &password)
	pg, volume := uuid.New(), uuid.New()
	image := domain.PostgresImage
	spec := h.spec
	spec.Applications = append(append([]domain.Application{}, spec.Applications...), domain.Application{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: password.Id, Roles: domain.PostgresOwner}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}})
	spec.Volumes = []domain.Volume{{ID: volume, Name: "Data"}}
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	environments := countInserts(t, h, "environments")
	preview := h.waitPreview(t, "approval_required")
	h.b.call("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	preview = h.waitPreview(t, "failed")
	if !strings.HasPrefix(preview.Note, "Preview environment was not created: Update the agent on Preview host") || !strings.HasSuffix(preview.Note, refusalRetry) || preview.EnvironmentId != nil {
		t.Fatal("the preview did not say its host's agent needs an update, or kept an environment", preview.Note)
	}
	stillRefused(t, h, preview, []func() int64{environments}, true)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/heartbeat", h.token, api.AgentHeartbeat{Capabilities: current, Preflight: h.enrolled.Preflight}, 200, nil)
	h.pullRequestHook(t)
	if preview = h.waitPreview(t, "building"); preview.EnvironmentId == nil {
		t.Fatal("the updated agent's preview has no environment")
	}
}

// A build refused for a preview's existing environment is not admitted again, nor its environment's
// secrets searched again, until the request's commit changes.
func TestRefusedPreviewBuildWaitsForANewCommit(t *testing.T) {
	h := newHungPreview(t)
	const canary = "preview-existing-canary"
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, &secret)
	spec := h.spec
	spec.Variables = append(append([]domain.Variable{}, spec.Variables...), domain.Variable{Name: "TOKEN", SecretVersionID: &secret.Id})
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := approvePreview(t, h, h.path)
	// The first commit's build ends, and the preview's frozen arguments come to hold the secret,
	// which the next build's check finds in the environment's copy of it.
	first := nextPreviewCommand(t, h)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: first.Id, Generation: first.Generation, PlanHash: first.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	h.waitPreview(t, "failed")
	execSQL(t, h.hungBuild, "UPDATE preview_requests SET configuration=jsonb_set(configuration,'{build,arguments}',jsonb_build_object('TOKEN',$1::text)) WHERE id=$2", canary, preview.Id)
	pipelines := countInserts(t, h, "execution_operations")
	opened := secretAccesses(t, h)
	pushPreviewCommit(t, h, strings.Repeat("b", 40))
	preview = h.waitPreview(t, "failed")
	if preview.Note != "Preview build rejected before runner delivery: Public build arguments contain a known secret value."+refusalRetry || preview.EnvironmentId == nil {
		t.Fatal("the build was not refused in the preview's environment", preview.Note)
	}
	if secretAccesses(t, h) == opened {
		t.Fatal("the refused build searched no secrets")
	}
	if pipelines() != 0 {
		t.Fatal("the refused build was admitted")
	}
	stillRefused(t, h, preview, nil, true)
	opened = secretAccesses(t, h)
	pushPreviewCommit(t, h, strings.Repeat("c", 40))
	if again := h.waitPreview(t, "failed"); again.Head != strings.Repeat("c", 40) || secretAccesses(t, h) == opened {
		t.Fatal("the new commit's build was not checked", again.Head)
	}
}

// refuseRevisions has PostgreSQL refuse the revisions written for env, as it refuses text it cannot
// store, and counts each one tried. It returns the count and a function that stops the refusal.
func refuseRevisions(t *testing.T, h *hungPreview, env uuid.UUID) (func() int64, func()) {
	t.Helper()
	execSQL(t, h.hungBuild, fmt.Sprintf(`CREATE SEQUENCE refused_revisions;
		CREATE FUNCTION refuse_revisions() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
			IF NEW.environment_id = '%s' THEN PERFORM nextval('refused_revisions'); RAISE EXCEPTION 'unstorable' USING ERRCODE = '22P05'; END IF;
			RETURN NEW;
		END$$;
		CREATE TRIGGER refuse_revisions BEFORE INSERT ON specification_revisions FOR EACH ROW EXECUTE FUNCTION refuse_revisions()`, env))
	count := func() int64 {
		t.Helper()
		var n int64
		var called bool
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT last_value,is_called FROM refused_revisions").Scan(&n, &called); err != nil {
			t.Fatal(err)
		}
		if !called {
			return 0
		}
		return n
	}
	return count, func() { execSQL(t, h.hungBuild, "DROP TRIGGER refuse_revisions ON specification_revisions") }
}

// A refused promotion is not tried again until the request's commit changes: a later pass that
// would now promote the build leaves the preview refused, and a new commit's build deploys.
func TestRefusedPreviewPromotionWaitsForANewCommit(t *testing.T) {
	h := newHungPreview(t)
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := approvePreview(t, h, h.path)
	promotions, accept := refuseRevisions(t, h, *preview.EnvironmentId)
	reportPreviewBuild(t, h)
	preview = h.waitPreview(t, "failed")
	if preview.Note != "Preview was not deployed: the configuration would be invalid."+refusalRetry {
		t.Fatalf("the preview's note is %q", preview.Note)
	}
	stillRefused(t, h, preview, []func() int64{promotions}, false)
	// Promoting would pass now, but nothing the promotion depends on has changed.
	accept()
	if again := previewPass(t, h, preview); again.State != "failed" || again.Note != preview.Note {
		t.Fatal("a pass promoted the refused build again", again.State, again.Note)
	}
	pushPreviewCommit(t, h, strings.Repeat("b", 40))
	h.waitPreview(t, "building")
	reportPreviewBuild(t, h)
	if deployed := h.waitPreview(t, "deploying"); deployed.Head != strings.Repeat("b", 40) {
		t.Fatal("another commit's build was deployed", deployed.Head)
	}
}
