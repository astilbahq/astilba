package integration

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

// revoke ends token at GitHub, as its holder can.
func (h *heldGitHub) revoke(t *testing.T, token string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "DELETE", h.f.execution.Policy.GitHubFixture+"/installation/token", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal("revocation refused", res.StatusCode)
	}
}

func (h *heldGitHub) accepts(token string) bool {
	_, revoked := h.revokedTokens.Load(token)
	return !revoked
}

func deliveredToken(t *testing.T, c *api.ExecutionCommand) string {
	t.Helper()
	var p pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Access == nil || !strings.HasPrefix(p.Access.Clone.Password, "installation-read-canary-") {
		t.Fatal("the build was delivered without a read token")
	}
	return p.Access.Clone.Password
}

// A runner, or code its build runs, can revoke the GitHub token it receives (PLAT-401). That
// token is its own, so management's calls and the next runner's clone go on working. A token of
// management's own that GitHub stops accepting is dropped, and the next call mints another.
func TestARunnerRevokingItsGitHubTokenAffectsOnlyItself(t *testing.T) {
	h := newHungBuild(t)
	github := heldGitHubFor(t, h.f, h.b, h.ws)
	config := h.config
	config.ConnectionID = github.connect(t)
	bump := func() {
		// A new generation misses the five-second cache of the last validation, so management
		// checks the repository again with its own token.
		execSQL(t, h, "UPDATE github_app_connections SET generation=generation+1 WHERE connection_id=$1", config.ConnectionID)
	}
	first := h.deliver(t, config)
	revoked := deliveredToken(t, first)
	github.revoke(t, revoked)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: first.Id, Generation: first.Generation, PlanHash: first.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)

	bump()
	second := h.deliver(t, config)
	next := deliveredToken(t, second)
	own, _ := github.repositoryToken.Load().(string)
	if next == revoked || !github.accepts(next) || own == "" || own == revoked || own == next || !github.accepts(own) {
		t.Fatal("a revoked runner token reached management or the next runner")
	}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: second.Id, Generation: second.Generation, PlanHash: second.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)

	// The request that meets the rejected token says to try again, and the next one works.
	github.revoke(t, own)
	bump()
	refused := h.b.call("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": config}, 503, nil)
	if !strings.Contains(string(refused), "Try again") {
		t.Fatal("a rejected token was not reported as passing", string(refused))
	}
	minted := github.minted.Load()
	bump()
	h.createBuild(t, config)
	if again, _ := github.repositoryToken.Load().(string); again == own || !github.accepts(again) || github.minted.Load() == minted {
		t.Fatal("management kept a token GitHub rejected")
	}
}

// Each delivery mints its runner's token, so many builds at once can meet GitHub's rate limits.
// The delivery then says why it waits, asks GitHub no sooner than GitHub said to, and goes once
// GitHub allows it.
func TestARateLimitedDeliveryWaitsAndSaysWhy(t *testing.T) {
	h := newHungBuild(t)
	github := heldGitHubFor(t, h.f, h.b, h.ws)
	config := h.config
	config.ConnectionID = github.connect(t)
	op := h.createBuild(t, config)
	github.limitMints.Store(1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	command, _ := pollReleased(h.f.execution, ctx, h.identity)
	cancel()
	if command != nil || h.deliveryStarted(t, op.Id) {
		t.Fatal("a build was delivered without its runner's token")
	}
	var state, note string
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note FROM execution_operations WHERE id=$1", op.Id).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != "dispatched" || !strings.Contains(note, "GitHub limited how fast") {
		t.Fatalf("a rate-limited delivery did not wait and say why: %s %q", state, note)
	}
	github.limitMints.Store(0)
	ctx, cancel = context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command, err := pollReleased(h.f.execution, ctx, h.identity)
	if err != nil || command == nil || command.Id != op.Id {
		t.Fatal("the build was not delivered once GitHub allowed it", err)
	}
	deliveredToken(t, command)
}

// A delivery that waits on GitHub until its deadline expires undelivered, and still says why.
func TestARateLimitedDeliveryThatExpiresSaysWhy(t *testing.T) {
	h := newHungBuild(t)
	github := heldGitHubFor(t, h.f, h.b, h.ws)
	config := h.config
	config.ConnectionID = github.connect(t)
	op := h.createBuild(t, config)
	github.limitMints.Store(1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	_, _ = pollReleased(h.f.execution, ctx, h.identity)
	cancel()
	execSQL(t, h, "UPDATE execution_operations SET command=jsonb_set(command,'{deadline}','\"2000-01-01T00:00:00Z\"') WHERE id=$1", op.Id)
	ctx, cancel = context.WithTimeout(t.Context(), 3*time.Second)
	_, _ = pollReleased(h.f.execution, ctx, h.identity)
	cancel()
	var state, note string
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note FROM execution_operations WHERE id=$1", op.Id).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || !strings.Contains(note, "expired before runner delivery") || !strings.Contains(note, "GitHub limited how fast") {
		t.Fatalf("an expired rate-limited delivery lost why it waited: %s %q", state, note)
	}
}

// A runner that asks for its pipeline again gets the pipeline's own token back, so asking again
// and again mints nothing more.
func TestARedeliveryGetsThePipelinesOwnToken(t *testing.T) {
	h := newHungBuild(t)
	github := heldGitHubFor(t, h.f, h.b, h.ws)
	config := h.config
	config.ConnectionID = github.connect(t)
	first := h.deliver(t, config)
	token := deliveredToken(t, first)
	minted := github.minted.Load()
	for range 3 {
		agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/releases", h.token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: first.Id, Epoch: first.SecretRelease.Epoch, Reason: api.JournalMissing}}}, 204, nil)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		again, err := pollReleased(h.f.execution, ctx, h.identity)
		cancel()
		if err != nil || again == nil || again.Id != first.Id || deliveredToken(t, again) != token {
			t.Fatal("a redelivery did not carry the pipeline's own token", err)
		}
	}
	if github.minted.Load() != minted {
		t.Fatal("redeliveries minted more tokens", github.minted.Load()-minted)
	}
}

// A submission that meets GitHub's rate limit waits for the time GitHub gives, saying so, and
// spends none of its tries meanwhile, rather than spend them all before the limit lifts.
func TestARateLimitedSubmissionWaitsForGitHub(t *testing.T) {
	h := newHeldGitHub(t)
	f, b := h.f, h.b
	connection := h.connect(t)
	var project api.Project
	b.call("POST", h.prefix+"/projects", map[string]string{"name": "App"}, 201, &project)
	var env api.Environment
	b.call("POST", h.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	path := h.prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	yes := true
	enrolled, in, token := enrollment(t, f, b, h.ws, "build")
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	runner := authorityAgent{t, f, token, in}
	head := strings.Repeat("a", 40)
	h.tree.record(head, importHonest)
	var op api.Operation
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: connection, Branch: "main", RunnerID: enrolled.Server.Id, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, &op)
	runner.reportImport(runner.next(), head, importHonest)
	draft, spec := draftSpec(t, b, path)
	spec.Compose.Files[0].Content += "# edited\n"
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	h.limitMints.Store(600)
	var submission api.SourceSubmission
	b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 202, &submission)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var state, note, job string
		var attempt int
		if err := f.st.Pool.QueryRow(t.Context(), "SELECT s.state,s.note,j.state,j.attempt FROM source_submissions s, river_job j WHERE s.id=$1 AND j.kind='platform_source_submission_v1' ORDER BY j.id DESC LIMIT 1", submission.Id).Scan(&state, &note, &job, &attempt); err != nil {
			t.Fatal(err)
		}
		// The note is written before River records the snooze.
		if strings.Contains(note, "Waiting until GitHub allows requests again") && job != "running" {
			if state != "queued" || job != "scheduled" || attempt != 0 {
				t.Fatalf("a rate-limited submission did not wait for GitHub: %s %s attempt %d", state, job, attempt)
			}
			return
		}
		if state != "queued" || time.Now().After(deadline) {
			t.Fatalf("a rate-limited submission did not wait for GitHub: %s %q", state, note)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
