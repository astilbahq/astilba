package integration

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// heldGitea is a tenant's own Git server, whose chosen requests hang until released.
type heldGitea struct {
	server     *httptest.Server
	head       atomic.Value
	repository atomic.Int64
	pullHead   atomic.Value
	pullClosed atomic.Bool
	onRequest  atomic.Pointer[func(path string)]

	mu       sync.Mutex
	match    string
	skip     int
	once     bool
	failHeld bool
	failing  string
	gate     chan struct{}
	entered  chan struct{}
	asked    map[string]int
}

func newHeldGitea(t *testing.T) *heldGitea {
	h := &heldGitea{asked: map[string]int{}}
	h.head.Store(strings.Repeat("a", 40))
	h.pullHead.Store(strings.Repeat("a", 40))
	h.repository.Store(12)
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read before waiting: a held request answers with what was true when it was asked.
		repository, head, pullHead, pullState := h.repository.Load(), h.head.Load().(string), h.pullHead.Load().(string), "open"
		if h.pullClosed.Load() {
			pullState = "closed"
		}
		if hook := h.onRequest.Load(); hook != nil {
			(*hook)(r.URL.Path)
		}
		h.mu.Lock()
		h.asked[r.URL.Path]++
		gate, entered := h.gate, h.entered
		held := gate != nil && strings.HasSuffix(r.URL.Path, h.match)
		failing := h.failing != "" && strings.HasSuffix(r.URL.Path, h.failing)
		if held && h.skip > 0 {
			h.skip--
			held = false
		}
		if held && h.once {
			h.gate = nil
		}
		h.mu.Unlock()
		if held {
			entered <- struct{}{}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			h.mu.Lock()
			fail := h.failHeld
			h.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		}
		if failing {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/branches/"):
			_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": head}})
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			_ = stablejson.Encode(w, map[string]any{"number": 7, "state": pullState, "base": map[string]any{"ref": "main", "repo": map[string]any{"id": 12}}, "head": map[string]any{"sha": pullHead, "repo": map[string]any{"id": 99}}})
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = stablejson.Encode(w, []map[string]int{{"number": 7}})
		default:
			_ = stablejson.Encode(w, map[string]any{"id": repository, "private": false})
		}
	}))
	t.Cleanup(h.server.Close)
	return h
}

// hold makes requests whose path ends with match hang until the returned release runs.
func (h *heldGitea) hold(t *testing.T, match string) (<-chan struct{}, func()) {
	t.Helper()
	gate, entered := make(chan struct{}), make(chan struct{}, 64)
	h.mu.Lock()
	h.match, h.gate, h.entered = match, gate, entered
	h.mu.Unlock()
	release := sync.OnceFunc(func() {
		h.mu.Lock()
		h.gate = nil
		h.mu.Unlock()
		close(gate)
	})
	t.Cleanup(release)
	return entered, release
}

// holdAfter is hold for a request after the next few that match.
func (h *heldGitea) holdAfter(t *testing.T, match string, passed int) (<-chan struct{}, func()) {
	t.Helper()
	h.mu.Lock()
	h.skip = passed
	h.mu.Unlock()
	return h.hold(t, match)
}

// holdOnce is hold for the next matching request only.
func (h *heldGitea) holdOnce(t *testing.T, match string) (<-chan struct{}, func()) {
	t.Helper()
	h.mu.Lock()
	h.once = true
	h.mu.Unlock()
	return h.hold(t, match)
}

func (h *heldGitea) failReleased() {
	h.mu.Lock()
	h.failHeld = true
	h.mu.Unlock()
}

// fail makes every request whose path ends with match fail at once, until the returned restore runs.
func (h *heldGitea) fail(t *testing.T, match string) func() {
	t.Helper()
	h.mu.Lock()
	h.failing = match
	h.mu.Unlock()
	end := sync.OnceFunc(func() {
		h.mu.Lock()
		h.failing = ""
		h.mu.Unlock()
	})
	t.Cleanup(end)
	return end
}

func (h *heldGitea) requests(match string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for path, count := range h.asked {
		if strings.Contains(path, match) {
			total += count
		}
	}
	return total
}

type reply struct {
	status int
	body   []byte
	err    error
}

// send makes a browser call that can wait behind a hung provider, for the test to judge on
// its own goroutine.
func (b *browser) send(method, path string, body any) <-chan reply {
	out := make(chan reply, 1)
	go func() {
		raw, _ := stablejson.Marshal(body)
		request, err := http.NewRequest(method, b.f.server.URL+path, bytes.NewReader(raw))
		if err != nil {
			out <- reply{err: err}
			return
		}
		request.Header.Set("Origin", b.f.server.URL)
		request.Header.Set("X-CSRF-Token", b.csrf)
		request.Header.Set("Content-Type", "application/json")
		response, err := b.client.Do(request)
		if err != nil {
			out <- reply{err: err}
			return
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		out <- reply{response.StatusCode, data, err}
	}()
	return out
}

func requireReply(t *testing.T, r reply, status int) []byte {
	t.Helper()
	if r.err != nil || r.status != status {
		t.Fatalf("status %d want %d: %s %v", r.status, status, r.body, r.err)
	}
	return r.body
}

func waitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("the call never reached the provider")
	}
}

// hungBuild is a workspace with one build runner and one application.
type hungBuild struct {
	f          *fixture
	b          *browser
	provider   *heldGitea
	ws, app    uuid.UUID
	connection pipeline.Connection
	secret     string
	prefix     string
	path       string
	draft      api.Revision
	spec       domain.Specification
	config     pipeline.BuildConfig
	server     uuid.UUID
	identity   execution.Identity
	token      string
	enrolled   api.AgentEnrollment
}

func newHungBuild(t *testing.T) *hungBuild {
	t.Helper()
	f := setup(t)
	b := f.browser(t)
	h := &hungBuild{f: f, b: b, provider: newHeldGitea(t), app: uuid.New()}
	h.ws = b.login("owner@example.test", f.password).Workspaces[0].Id
	h.prefix = "/api/v1/workspaces/" + h.ws.String()
	var connection struct {
		Connection pipeline.Connection `json:"connection"`
		Secret     string              `json:"webhookSecret"`
	}
	b.call("POST", h.prefix+"/git-connections", map[string]any{"name": "Held", "provider": "gitea", "baseUrl": h.provider.server.URL, "repository": "team/app", "clone": map[string]string{"username": "reader", "password": "clone-canary"}}, 201, &connection)
	h.connection, h.secret = connection.Connection, connection.Secret
	var project api.Project
	b.call("POST", h.prefix+"/projects", map[string]string{"name": "Held"}, 201, &project)
	var env api.Environment
	b.call("POST", h.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Held"}, 201, &env)
	h.path = h.prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	image := pipelineTestImage
	h.spec = domain.Specification{Version: 1, Applications: []domain.Application{{ID: h.app, Name: "web", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
	b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": h.spec}, 200, &h.draft)
	enrolled, in, token := enrollment(t, f, b, h.ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	h.server, h.token, h.enrolled = enrolled.Server.Id, token, in
	var err error
	if h.identity, err = f.execution.Authenticate(t.Context(), h.server, in.Credential); err != nil {
		t.Fatal(err)
	}
	h.config = pipeline.BuildConfig{ConnectionID: h.connection.ID, Branch: "main", RunnerID: h.server, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
	return h
}

func (h *hungBuild) createBuild(t *testing.T, config pipeline.BuildConfig) api.Operation {
	t.Helper()
	var op api.Operation
	h.b.call("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": config}, 202, &op)
	h.waitState(t, op.Id, "dispatched")
	return op
}

func (h *hungBuild) waitState(t *testing.T, operation uuid.UUID, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var state string
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state FROM execution_operations WHERE id=$1", operation).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("operation never reached", want, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// requireNothingHeld shows, while a provider request hangs, that its operation holds no
// lock anything else waits for: NOWAIT fails at once on a row another transaction holds.
func (h *hungBuild) requireNothingHeld(t *testing.T, operation uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, row := range []struct {
		name, statement string
		id              uuid.UUID
	}{
		{"server", "SELECT 1 FROM servers WHERE id=$1 FOR UPDATE NOWAIT", h.server},
		{"connection", "SELECT 1 FROM git_connections WHERE id=$1 FOR UPDATE NOWAIT", h.connection.ID},
		{"workspace", "SELECT 1 FROM workspaces WHERE id=$1 FOR NO KEY UPDATE NOWAIT", h.ws},
		{"operation", "SELECT 1 FROM execution_operations WHERE id=$1 FOR UPDATE NOWAIT", operation},
	} {
		// The dispatcher takes these for a moment every two seconds. A hold across the call
		// lasts until the test releases it, so it outlives these tries.
		for attempt := 1; ; attempt++ {
			tx, err := h.f.st.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, row.statement, row.id)
			_ = tx.Rollback(ctx)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "55P03" {
				if attempt == 20 {
					t.Fatalf("the %s row is held across a provider call", row.name)
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	requireNoOpenTransaction(t, ctx, h.f)
	// The queue in front of the workspace lock is free, not only the lock.
	_, done, err := h.f.st.BeginWorkspaceWrite(ctx, h.ws)
	if err != nil {
		t.Fatal("the workspace queue is held across a provider call:", err)
	}
	done()
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/heartbeat", h.token, api.AgentHeartbeat{Capabilities: h.enrolled.Capabilities, Preflight: h.enrolled.Preflight}, 200, nil)
}

// requireNoOpenTransaction catches a transaction left open across a call, which pins a pool
// connection with no lock to see. The dispatcher's own are brief, so one reading of none will do.
func requireNoOpenTransaction(t *testing.T, ctx context.Context, f *fixture) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		var open int
		if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND state='idle in transaction'").Scan(&open); err != nil {
			t.Fatal(err)
		}
		if open == 0 {
			return
		}
		if attempt == 40 {
			t.Fatal("a transaction is open across a provider call")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *hungBuild) deliveryStarted(t *testing.T, operation uuid.UUID) bool {
	t.Helper()
	var started *bool
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT delivery_started FROM pipeline_runs WHERE id=$1", operation).Scan(&started); err != nil {
		t.Fatal(err)
	}
	return started != nil && *started
}

type polled struct {
	command *api.ExecutionCommand
	err     error
}

func (h *hungBuild) pollAsync(ctx context.Context) <-chan polled {
	out := make(chan polled, 1)
	go func() {
		command, err := h.f.execution.Poll(ctx, h.identity)
		out <- polled{command, err}
	}()
	return out
}

func carriesCredential(c *api.ExecutionCommand) bool {
	return c != nil && c.Pipeline != nil && bytes.Contains(*c.Pipeline, []byte("clone-canary"))
}

// A slow provider must not stop the runner's heartbeats, the workspace's writers or anyone
// else's use of the pool. Whatever changed during the call decides what happens next: the
// agent's credential, the operation and the connection are each read again before a
// credential leaves management.
func TestDeliveryRechecksEverythingAfterAHungProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		// During runs while the provider hangs, against this operation.
		during func(t *testing.T, h *hungBuild, operation uuid.UUID)
		check  func(t *testing.T, h *hungBuild, operation uuid.UUID, result polled)
		// Finishes means the poll has nothing to return and waits out its long poll instead.
		finishes bool
	}{
		{
			name:   "nothing changed",
			during: func(*testing.T, *hungBuild, uuid.UUID) {},
			check: func(t *testing.T, h *hungBuild, operation uuid.UUID, result polled) {
				if result.err != nil || !carriesCredential(result.command) || !h.deliveryStarted(t, operation) {
					t.Fatal("an unchanged delivery was lost", result.err)
				}
			},
		},
		{
			name: "agent revoked",
			during: func(t *testing.T, h *hungBuild, _ uuid.UUID) {
				execSQL(t, h, "UPDATE servers SET revoked=true WHERE id=$1", h.server)
			},
			check: refusedAuthentication,
		},
		{
			name: "agent's credential rotated away",
			during: func(t *testing.T, h *hungBuild, _ uuid.UUID) {
				execSQL(t, h, "UPDATE servers SET credential_hash=sha256('rotated'),previous_hash=NULL WHERE id=$1", h.server)
			},
			check: refusedAuthentication,
		},
		{
			name: "server transferred to another controller",
			during: func(t *testing.T, h *hungBuild, _ uuid.UUID) {
				execSQL(t, h, "UPDATE servers SET awaiting_transfer=true WHERE id=$1", h.server)
			},
			check: refusedAuthentication,
		},
		{
			name:     "operation cancelled",
			finishes: true,
			during: func(t *testing.T, h *hungBuild, operation uuid.UUID) {
				execSQL(t, h, "UPDATE execution_operations SET cancel_requested=true WHERE id=$1", operation)
			},
			check: stoppedBeforeDelivery,
		},
		{
			// Hosted with no entitlement row is a lapsed subscription. Setting it only now keeps
			// the dispatcher from refusing the command before the poll reaches the provider.
			name:     "subscription lapsed",
			finishes: true,
			during:   func(t *testing.T, h *hungBuild, _ uuid.UUID) { h.f.st.Hosted = true },
			check:    stoppedBeforeDelivery,
		},
		{
			name: "connection repointed",
			during: func(t *testing.T, h *hungBuild, _ uuid.UUID) {
				// The held answer still says 12, which matches the frozen command. Only the
				// answer to the question asked again, about the new state, can refuse this.
				h.provider.repository.Store(13)
				execSQL(t, h, `UPDATE git_connections SET configuration=configuration||'{"name":"Repointed"}' WHERE id=$1`, h.connection.ID)
			},
			check: func(t *testing.T, h *hungBuild, operation uuid.UUID, result polled) {
				if result.err == nil || carriesCredential(result.command) || h.deliveryStarted(t, operation) {
					t.Fatal("an answer about the old connection authorized delivery", result.err)
				}
			},
		},
		{
			name: "failure while recording the delivery",
			during: func(t *testing.T, h *hungBuild, _ uuid.UUID) {
				execSQL(t, h, `CREATE FUNCTION refuse_run() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$ LANGUAGE plpgsql`)
				execSQL(t, h, `CREATE TRIGGER refuse_run BEFORE UPDATE ON pipeline_runs FOR EACH ROW EXECUTE FUNCTION refuse_run()`)
			},
			check: func(t *testing.T, h *hungBuild, operation uuid.UUID, result polled) {
				if result.err == nil || carriesCredential(result.command) || h.deliveryStarted(t, operation) {
					t.Fatal("a delivery that could not be recorded left management", result.err)
				}
				h.requireNothingHeld(t, operation)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungBuild(t)
			op := h.createBuild(t, h.config)
			entered, release := h.provider.hold(t, "/repos/team/app")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			result := h.pollAsync(ctx)
			waitEntered(t, entered)
			h.requireNothingHeld(t, op.Id)
			tc.during(t, h, op.Id)
			release()
			if tc.finishes {
				h.waitState(t, op.Id, "cancelled")
				cancel()
			}
			tc.check(t, h, op.Id, <-result)
		})
	}
}

func stoppedBeforeDelivery(t *testing.T, h *hungBuild, operation uuid.UUID, result polled) {
	t.Helper()
	var state string
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state FROM execution_operations WHERE id=$1", operation).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if carriesCredential(result.command) || h.deliveryStarted(t, operation) || state != "cancelled" {
		t.Fatal("a command that stopped being allowed during the call was still delivered", state, result.err)
	}
}

func refusedAuthentication(t *testing.T, h *hungBuild, operation uuid.UUID, result polled) {
	t.Helper()
	if !errors.Is(result.err, domain.Unauthenticated) || carriesCredential(result.command) || h.deliveryStarted(t, operation) {
		t.Fatal("delivery ignored what happened to the agent during the call", result.err)
	}
}

func execSQL(t *testing.T, h *hungBuild, statement string, args ...any) {
	t.Helper()
	if _, err := h.f.st.Pool.Exec(t.Context(), statement, args...); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryGivesUpOnAConnectionThatKeepsChanging(t *testing.T) {
	h := newHungBuild(t)
	op := h.createBuild(t, h.config)
	var changes atomic.Int64
	change := func(path string) {
		if strings.HasSuffix(path, "/repos/team/app") {
			name := "Renamed" + string(rune('A'+changes.Add(1)))
			_, _ = h.f.st.Pool.Exec(context.Background(), `UPDATE git_connections SET configuration=jsonb_set(configuration,'{name}',to_jsonb($2::text)) WHERE id=$1`, h.connection.ID, name)
		}
	}
	h.provider.onRequest.Store(&change)
	before := h.provider.requests("/repos/team/app")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command, err := h.f.execution.Poll(ctx, h.identity)
	if !errors.Is(err, domain.Conflict) || carriesCredential(command) || h.deliveryStarted(t, op.Id) {
		t.Fatal("a connection that never settled reached delivery", err)
	}
	if asked := h.provider.requests("/repos/team/app") - before; asked > 8 {
		t.Fatal("a changing connection was asked about without limit", asked)
	}
}

// A provider failure is the provider's answer, and must leave nothing delivered either.
func TestDeliveryStopsWhenTheProviderFailsAfterHanging(t *testing.T) {
	h := newHungBuild(t)
	op := h.createBuild(t, h.config)
	entered, release := h.provider.hold(t, "/repos/team/app")
	h.provider.failReleased()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	result := h.pollAsync(ctx)
	waitEntered(t, entered)
	release()
	outcome := <-result
	if outcome.err == nil || carriesCredential(outcome.command) || h.deliveryStarted(t, op.Id) {
		t.Fatal("a failed provider check still delivered", outcome.err)
	}
	if c, err := h.f.execution.Poll(ctx, h.identity); err != nil || !carriesCredential(c) {
		t.Fatal("delivery did not recover with the provider", err)
	}
}

func (h *hungBuild) deliver(t *testing.T, config pipeline.BuildConfig) *api.ExecutionCommand {
	t.Helper()
	op := h.createBuild(t, config)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command, err := h.f.execution.Poll(ctx, h.identity)
	if err != nil || command == nil || command.Id != op.Id {
		t.Fatal("build was not delivered", command, err)
	}
	return command
}

func (h *hungBuild) succeeded(t *testing.T, c *api.ExecutionCommand) api.AgentReport {
	t.Helper()
	raw, _ := stablejson.Marshal(pipeline.Result{Commit: h.provider.head.Load().(string), Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("b", 64), Architecture: "arm64"})
	message := jsontext.Value(raw)
	return api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &message}
}

// A build's report checks the branch before promoting the artifact into the draft. That call
// must not hold the runner's row, and whatever changed during it must decide the outcome.
func TestBuildReportRechecksAfterAHungProvider(t *testing.T) {
	automatic := func(h *hungBuild) pipeline.BuildConfig {
		config := h.config
		config.AutoBuild, config.AutoDeploy = true, true
		return config
	}
	counts := func(t *testing.T, h *hungBuild, operation uuid.UUID) (string, int, int) {
		t.Helper()
		var state string
		var artifacts, deployments int
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,(SELECT count(*) FROM build_artifacts WHERE id=$1),(SELECT count(*) FROM pipeline_auto_deployments WHERE id=$1) FROM execution_operations WHERE id=$1", operation).Scan(&state, &artifacts, &deployments); err != nil {
			t.Fatal(err)
		}
		return state, artifacts, deployments
	}
	for _, tc := range []struct {
		name                   string
		during                 func(t *testing.T, h *hungBuild, operation uuid.UUID)
		state                  string
		artifacts, deployments int
	}{
		{name: "nothing changed", during: func(*testing.T, *hungBuild, uuid.UUID) {}, state: "succeeded", artifacts: 1, deployments: 1},
		{
			name: "draft replaced",
			during: func(t *testing.T, h *hungBuild, _ uuid.UUID) {
				// An operator's edit proceeds during the call, and is not overwritten by it.
				h.spec.Applications[0].Name = "edited"
				h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": h.spec}, 200, nil)
			},
			state: "succeeded", artifacts: 1, deployments: 0,
		},
		{
			name: "operation cancelled",
			during: func(t *testing.T, h *hungBuild, operation uuid.UUID) {
				execSQL(t, h, "UPDATE execution_operations SET cancel_requested=true WHERE id=$1", operation)
			},
			state: "cancelled", artifacts: 0, deployments: 0,
		},
		{
			// The agent reported in time, so a slow provider must not cost it the result.
			name: "deadline passed during the call",
			during: func(t *testing.T, h *hungBuild, operation uuid.UUID) {
				execSQL(t, h, "UPDATE execution_operations SET command=jsonb_set(command,'{deadline}',to_jsonb($2::text)) WHERE id=$1", operation, time.Now().UTC().Format(time.RFC3339Nano))
			},
			state: "succeeded", artifacts: 1, deployments: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungBuild(t)
			c := h.deliver(t, automatic(h))
			entered, release := h.provider.hold(t, "/branches/main")
			finished := make(chan error, 1)
			go func() { finished <- h.f.execution.Report(context.Background(), h.identity, h.succeeded(t, c)) }()
			waitEntered(t, entered)
			h.requireNothingHeld(t, c.Id)
			tc.during(t, h, c.Id)
			release()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			state, artifacts, deployments := counts(t, h, c.Id)
			if state != tc.state || artifacts != tc.artifacts || deployments != tc.deployments {
				t.Fatal("report outcome", state, artifacts, deployments)
			}
		})
	}
	t.Run("provider failure leaves the report to retry", func(t *testing.T) {
		h := newHungBuild(t)
		c := h.deliver(t, automatic(h))
		entered, release := h.provider.hold(t, "/branches/main")
		h.provider.failReleased()
		finished := make(chan error, 1)
		go func() { finished <- h.f.execution.Report(context.Background(), h.identity, h.succeeded(t, c)) }()
		waitEntered(t, entered)
		release()
		if err := <-finished; err == nil {
			t.Fatal("a failed branch check was accepted")
		}
		if state, artifacts, deployments := counts(t, h, c.Id); state == "succeeded" || artifacts != 0 || deployments != 0 {
			t.Fatal("a failed report applied part of its result", state, artifacts, deployments)
		}
		if err := h.f.execution.Report(t.Context(), h.identity, h.succeeded(t, c)); err != nil {
			t.Fatal("the report did not recover with the provider", err)
		}
		if state, artifacts, deployments := counts(t, h, c.Id); state != "succeeded" || artifacts != 1 || deployments != 1 {
			t.Fatal("the retried report was incomplete", state, artifacts, deployments)
		}
	})
}
