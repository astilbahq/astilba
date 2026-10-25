package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/store"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func agentRequest(t *testing.T, c *http.Client, origin, path, token string, body any, status int, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	method := "POST"
	if body == nil {
		method = "GET"
	}
	req, _ := http.NewRequest(method, origin+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, e := c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != status {
		t.Fatalf("%s: %d want %d: %s", path, res.StatusCode, status, b)
	}
	if out != nil {
		if e = json.Unmarshal(b, out); e != nil {
			t.Fatal(e)
		}
	}
}
func enrollment(t *testing.T, f *fixture, b *browser, ws uuid.UUID, role string) (api.Enrollment, api.AgentEnrollment, string) {
	var v api.Enrollment
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers", map[string]string{"name": "Test server", "role": role}, 201, &v)
	credential, _ := cryptobox.Token()
	in := api.AgentEnrollment{ServerId: v.Server.Id, Token: v.Token, Credential: credential, MachineId: uuid.New(), Capabilities: api.AgentCapabilities{Protocol: 1, Version: "test", Os: "linux", Arch: "arm64", Docker: true}, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 1 << 30, Notes: []string{}}}
	return v, in, v.Server.Id.String() + "." + credential
}
func envForExecution(t *testing.T, f *fixture, b *browser, ws uuid.UUID) uuid.UUID {
	var p api.Project
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Probe"}, 201, &p)
	var env api.Environment
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects/"+p.Id.String()+"/environments", map[string]string{"name": "Test"}, 201, &env)
	return env.Id
}
func TestServerIdentityLifecycle(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	session := b.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	v, in, token := enrollment(t, f, b, ws, "deployment")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	httpClient := tlsServer.Client()
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	changed := in
	changed.Credential, _ = cryptobox.Token()
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/enroll", "", changed, 401, nil)
	changed = in
	changed.ServerId = uuid.New()
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/enroll", "", changed, 401, nil)
	// Agent tokens are not management identities, including on mutations.
	agentRequest(t, httpClient, tlsServer.URL, "/api/v1/workspaces/"+ws.String()+"/servers", token, nil, 401, nil)
	agentRequest(t, httpClient, tlsServer.URL, "/api/v1/workspaces/"+ws.String()+"/servers", token, map[string]string{"name": "forged", "role": "deployment"}, 401, nil)
	route := "/api/v1/workspaces/" + ws.String() + "/servers/" + v.Server.Id.String()
	b.call("POST", route+"/rotate", nil, 204, nil)
	next, _ := cryptobox.Token()
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/rotate", token, api.AgentRotation{Credential: next}, 204, nil)
	var expiry time.Time
	if e := f.st.Pool.QueryRow(context.Background(), "SELECT previous_until FROM servers WHERE id=$1", v.Server.Id).Scan(&expiry); e != nil {
		t.Fatal(e)
	}
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/rotate", token, api.AgentRotation{Credential: next}, 204, nil)
	var after time.Time
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT previous_until FROM servers WHERE id=$1", v.Server.Id).Scan(&after)
	if !after.Equal(expiry) {
		t.Fatal("retry extended rotation overlap")
	}
	another, _ := cryptobox.Token()
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/rotate", token, api.AgentRotation{Credential: another}, 401, nil)
	_, _ = f.st.Pool.Exec(context.Background(), "UPDATE servers SET previous_until=now()-interval '1 second' WHERE id=$1", v.Server.Id)
	heartbeat := api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/heartbeat", token, heartbeat, 401, nil)
	token = v.Server.Id.String() + "." + next
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/heartbeat", token, heartbeat, 200, nil)
	b.call("POST", route+"/revoke", nil, 204, nil)
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/heartbeat", token, heartbeat, 401, nil)
	var expired api.Enrollment
	b.call("POST", route+"/enrollment", nil, 201, &expired)
	_, _ = f.st.Pool.Exec(context.Background(), "UPDATE servers SET enrollment_expires=now()-interval '1 second' WHERE id=$1", v.Server.Id)
	in.Token = expired.Token
	in.Credential, _ = cryptobox.Token()
	agentRequest(t, httpClient, tlsServer.URL, "/agent/v1/enroll", "", in, 401, nil)
	var count int
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action LIKE 'server.%' AND actor_id=$2", ws, f.owner).Scan(&count)
	if count != 4 {
		t.Fatalf("identity audit events=%d", count)
	}
}
func TestDurableServerOperations(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	session := b.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	v, in, token := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	_, build, buildToken := enrollment(t, f, b, ws, "build")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", build, 200, nil)
	base := "/api/v1/workspaces/" + ws.String()
	b.call("POST", base+"/plans", api.PlanRequest{ServerId: build.ServerId, EnvironmentId: env, Action: "start"}, 403, nil)
	var plan api.ExecutionPlan
	b.call("POST", base+"/plans", api.PlanRequest{ServerId: v.Server.Id, EnvironmentId: env, Action: "start"}, 201, &plan)
	key := uuid.New()
	var op, again api.Operation
	b.call("POST", base+"/plans/"+plan.Id.String()+"/apply", api.ApplyPlan{IdempotencyKey: key}, 201, &op)
	b.call("POST", base+"/plans/"+plan.Id.String()+"/apply", api.ApplyPlan{IdempotencyKey: key}, 201, &again)
	if again.Id != op.Id {
		t.Fatal("apply was not idempotent")
	}
	var other api.ExecutionPlan
	b.call("POST", base+"/plans", api.PlanRequest{ServerId: v.Server.Id, EnvironmentId: env, Action: "remove"}, 201, &other)
	b.call("POST", base+"/plans/"+other.Id.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 409, nil)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state)
		if state == "dispatched" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	var poll api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &poll)
	if poll.Command == nil || poll.Command.Id != op.Id {
		t.Fatal("command missing")
	}
	var duplicate api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &duplicate)
	if duplicate.Command == nil || duplicate.Command.Id != op.Id {
		t.Fatal("lost poll response changed command")
	}
	report := api.AgentReport{Id: op.Id, Generation: op.Generation, PlanHash: poll.Command.PlanHash, Sequence: 1, State: "running", Steps: []api.ExecutionStep{}}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", buildToken, report, 404, nil)
	forged := report
	forged.PlanHash = strings.Repeat("0", 64)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, forged, 403, nil)
	b.call("POST", base+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &duplicate)
	if !duplicate.Command.CancelRequested {
		t.Fatal("explicit cancellation lost")
	}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, report, 204, nil)
	// Two queue clients can retry/finalize the same stable operation without another command.
	second, e := execution.New(f.st)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e = second.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer second.Stop(context.Background())
	report.Sequence = 2
	report.State = "cancelled"
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, report, 204, nil)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, report, 204, nil)
	old := report
	old.Sequence = 1
	old.State = "running"
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, old, 204, nil)
	var state string
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state)
	if state != "cancelled" {
		t.Fatal("out-of-order report regressed terminal result")
	}
	var count int
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM execution_operations WHERE workspace_id=$1", ws).Scan(&count)
	if count != 1 {
		t.Fatal("conflicting operation persisted")
	}
}
func TestAgentCrashRecoveryAndOwnership(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Docker")
	}
	docker, e := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if e != nil {
		t.Fatal(e)
	}
	defer docker.Close()
	for _, point := range []string{"accepted", "pull", "create", "created-effect", "start", "started-effect", "observe"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			j, e := agent.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			cfg := agent.Config{ServerID: uuid.New(), Role: "deployment"}
			if e = j.SetConfig(cfg); e != nil {
				t.Fatal(e)
			}
			env := uuid.New()
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: cfg.ServerID, EnvironmentId: env, ResourceId: uuid.NewSHA1(cfg.ServerID, []byte(env.String()+":probe:v1")), Generation: 1, PlanHash: strings.Repeat("a", 64), Kind: "test", Image: execution.TestImage, Action: "start", Deadline: time.Now().Add(time.Minute)}
			engine := &agent.Engine{Journal: j, Docker: docker, Point: func(p string) {
				if p == point {
					panic("crash checkpoint")
				}
			}}
			func() { defer func() { _ = recover() }(); _ = engine.Execute(context.Background(), c) }()
			j.Close()
			j, e = agent.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer j.Close()
			engine = &agent.Engine{Journal: j, Docker: docker}
			if e = engine.Execute(context.Background(), c); e != nil {
				t.Fatal(e)
			}
			r, e := j.Record(c.Id)
			if e != nil || r.Report.State != "succeeded" {
				t.Fatalf("recovery: %+v %v", r, e)
			}
			for _, step := range r.Report.Steps {
				if step.State != "done" {
					t.Fatalf("completed step still pending: %+v", step)
				}
			}
			again, e := j.Accept(c)
			if e != nil || again.Report.State != "succeeded" {
				t.Fatal("duplicate effect")
			}
			if e = j.Acknowledge(c.Id, r.Report.Sequence); e != nil {
				t.Fatal(e)
			}
			c.Id = uuid.New()
			c.Generation++
			c.Action = "remove"
			c.PlanHash = strings.Repeat("b", 64)
			if e = engine.Execute(context.Background(), c); e != nil {
				t.Fatal(e)
			}
			r, _ = j.Record(c.Id)
			if r.Report.State != "succeeded" {
				t.Fatalf("cleanup: %+v", r.Report)
			}
		})
	}
}
func TestAgentJournalFences(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	j, e := agent.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if other, e := agent.Open(dir); e == nil {
		other.Close()
		t.Fatal("second process opened state")
	}
	c := api.ExecutionCommand{Id: uuid.New(), EnvironmentId: uuid.New(), Generation: 1, PlanHash: strings.Repeat("a", 64)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() { _, e := j.Accept(c); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	changed := c
	changed.PlanHash = strings.Repeat("b", 64)
	if _, e = j.Accept(changed); e == nil {
		t.Fatal("changed command accepted")
	}
	changed = c
	changed.Id = uuid.New()
	changed.Generation++
	if _, e = j.Accept(changed); e == nil {
		t.Fatal("overlapping command accepted")
	}
}

func TestPollLeaseAndRevocation(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, token := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("GET", f.server.URL+"/agent/v1/poll", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, e := f.server.Client().Do(req)
		if e != nil {
			done <- 0
			return
		}
		res.Body.Close()
		done <- res.StatusCode
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var held bool
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT poll_id IS NOT NULL FROM servers WHERE id=$1", v.Server.Id).Scan(&held)
		if held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("poll did not take lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 409, nil)
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+v.Server.Id.String()+"/revoke", nil, 204, nil)
	select {
	case status := <-done:
		if status != 401 {
			t.Fatalf("revoked pending poll status %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not interrupt waiting poll")
	}
}

func TestOperationAdmissionRollbackAndIsolation(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	session := b.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	base := "/api/v1/workspaces/" + ws.String()
	var plan api.ExecutionPlan
	b.call("POST", base+"/plans", api.PlanRequest{ServerId: in.ServerId, EnvironmentId: env, Action: "start"}, 201, &plan)
	// Database fixture rejects queue insertion, proving operation, generation and audit share rollback.
	_, e := f.st.Pool.Exec(context.Background(), `CREATE FUNCTION reject_test_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture queue unavailable'; END; $$; CREATE TRIGGER reject_test_job BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION reject_test_job()`)
	if e != nil {
		t.Fatal(e)
	}
	b.call("POST", base+"/plans/"+plan.Id.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 500, nil)
	var generation, count, audits int
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT execution_generation FROM environments WHERE id=$1", env).Scan(&generation)
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM execution_operations").Scan(&count)
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='operation.apply'").Scan(&audits)
	if generation != 0 || count != 0 || audits != 0 {
		t.Fatalf("partial transaction %d %d %d", generation, count, audits)
	}
	_, _ = f.st.Pool.Exec(context.Background(), "DROP TRIGGER reject_test_job ON river_job; DROP FUNCTION reject_test_job()")
	var foreign api.Workspace
	b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Other"}, 201, &foreign)
	b.call("POST", "/api/v1/workspaces/"+foreign.Id.String()+"/plans", api.PlanRequest{ServerId: in.ServerId, EnvironmentId: env, Action: "start"}, 404, nil)
	b.call("POST", base+"/plans", api.PlanRequest{ServerId: in.ServerId, EnvironmentId: uuid.New(), Action: "start"}, 404, nil)
	// Concurrent admissions use different keys and the same reviewed plan: exactly one wins.
	account, e := f.st.Queries.AccountByID(context.Background(), f.owner)
	if e != nil {
		t.Fatal(e)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			_, e := f.execution.Apply(context.Background(), store.Principal{Account: account, Recent: true}, ws, plan.Id, uuid.New())
			results <- e
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent admission successes: %d", success)
	}
}

func TestP2EngineHelper(t *testing.T) {
	if os.Getenv("ASTILBA_P2_HELPER") != "1" {
		t.Skip("subprocess fixture")
	}
	j, e := agent.Open(os.Getenv("ASTILBA_P2_STATE"))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	docker, e := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if e != nil {
		t.Fatal(e)
	}
	defer docker.Close()
	var c api.ExecutionCommand
	if e = json.Unmarshal([]byte(os.Getenv("ASTILBA_P2_COMMAND")), &c); e != nil {
		t.Fatal(e)
	}
	engine := agent.Engine{Journal: j, Docker: docker, Point: func(p string) {
		if p == os.Getenv("ASTILBA_P2_POINT") {
			os.Exit(42)
		}
	}}
	if e = engine.Execute(context.Background(), c); e != nil {
		t.Fatal(e)
	}
}
func TestProcessExitAfterExternalEffect(t *testing.T) {
	if testing.Short() {
		t.Skip("disposable Docker subprocess")
	}
	for _, point := range []string{"accepted", "created-effect", "started-effect", "removed-effect"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			j, e := agent.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			server, env := uuid.New(), uuid.New()
			if e = j.SetConfig(agent.Config{ServerID: server, Role: "deployment"}); e != nil {
				t.Fatal(e)
			}
			j.Close()
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, ResourceId: uuid.NewSHA1(server, []byte(env.String()+":probe:v1")), Generation: 1, PlanHash: strings.Repeat("a", 64), Kind: "test", Image: execution.TestImage, Action: "start", Deadline: time.Now().Add(time.Minute)}
			run := func(point string, code int) {
				t.Helper()
				b, _ := json.Marshal(c)
				cmd := exec.Command(os.Args[0], "-test.run=^TestP2EngineHelper$")
				cmd.Env = append(os.Environ(), "ASTILBA_P2_HELPER=1", "ASTILBA_P2_STATE="+dir, "ASTILBA_P2_COMMAND="+string(b), "ASTILBA_P2_POINT="+point)
				output, e := cmd.CombinedOutput()
				if code == 0 && e != nil {
					t.Fatalf("helper: %s %v", output, e)
				}
				if code != 0 {
					var exit *exec.ExitError
					if !errors.As(e, &exit) || exit.ExitCode() != code {
						t.Fatalf("expected exit %d: %s %v", code, output, e)
					}
				}
			}
			if point == "removed-effect" {
				run("", 0)
				j, e = agent.Open(dir)
				if e != nil {
					t.Fatal(e)
				}
				r, _ := j.Record(c.Id)
				_ = j.Acknowledge(c.Id, r.Report.Sequence)
				j.Close()
				c.Id = uuid.New()
				c.Generation++
				c.Action = "remove"
			}
			run(point, 42)
			run("", 0)
			j, e = agent.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer j.Close()
			r, e := j.Record(c.Id)
			if e != nil || r.Report.State != "succeeded" {
				t.Fatalf("restarted result %+v %v", r.Report, e)
			}
			_ = j.Acknowledge(c.Id, r.Report.Sequence)
			if c.Action != "remove" {
				docker, e := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
				if e != nil {
					t.Fatal(e)
				}
				defer docker.Close()
				engine := agent.Engine{Journal: j, Docker: docker}
				c.Id = uuid.New()
				c.Generation++
				c.Action = "remove"
				if e = engine.Execute(context.Background(), c); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestUnownedContainerIsNeverRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("disposable Docker")
	}
	docker, e := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if e != nil {
		t.Fatal(e)
	}
	defer docker.Close()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	j, e := agent.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	server, env := uuid.New(), uuid.New()
	_ = j.SetConfig(agent.Config{ServerID: server, Role: "deployment"})
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, ResourceId: uuid.NewSHA1(server, []byte(env.String()+":probe:v1")), Generation: 1, PlanHash: strings.Repeat("a", 64), Kind: "test", Action: "remove", Image: execution.TestImage, Deadline: time.Now().Add(time.Minute)}
	marker := uuid.NewString()
	created, e := docker.ContainerCreate(context.Background(), client.ContainerCreateOptions{Name: agent.Name(c), Config: &container.Config{Image: execution.TestImage, Labels: map[string]string{"com.astilba.fixture": marker}}})
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		v, e := docker.ContainerInspect(context.Background(), created.ID, client.ContainerInspectOptions{})
		if e == nil && v.Container.Config.Labels["com.astilba.fixture"] == marker {
			_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
		}
	}()
	engine := agent.Engine{Journal: j, Docker: docker}
	if e = engine.Execute(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	r, _ := j.Record(c.Id)
	if r.Report.State != "needs_attention" {
		t.Fatalf("ownership mismatch result %s", r.Report.State)
	}
	if _, e = docker.ContainerInspect(context.Background(), created.ID, client.ContainerInspectOptions{}); e != nil {
		t.Fatal("unowned container removed", e)
	}
}

func TestAgentReportsAfterManagementLossAndRestart(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	enrollment, in, _ := enrollment(t, f, b, ws, "deployment")
	var outage atomic.Bool
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		f.server.Config.Handler.ServeHTTP(w, r)
	}))
	defer tlsServer.Close()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	j, e := agent.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.SetConfig(agent.Config{Origin: tlsServer.URL, ServerID: enrollment.Server.Id, MachineID: in.MachineId}); e != nil {
		t.Fatal(e)
	}
	docker, e := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if e != nil {
		t.Fatal(e)
	}
	defer docker.Close()
	created, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	engine := &agent.Engine{Journal: j, Docker: docker, Point: func(p string) {
		if p == "created-effect" {
			once.Do(func() { close(created); <-release })
		}
	}}
	agentClient := agent.NewClient(j, engine)
	agentClient.HTTP = tlsServer.Client()
	agentClient.HTTP.Timeout = 35 * time.Second
	agentClient.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
	}
	if e = agentClient.Enroll(context.Background(), enrollment.Token); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentClient.Run(ctx) }()
	defer cancel()
	var plan api.ExecutionPlan
	base := "/api/v1/workspaces/" + ws.String()
	b.call("POST", base+"/plans", api.PlanRequest{ServerId: in.ServerId, EnvironmentId: env, Action: "start"}, 201, &plan)
	var op api.Operation
	b.call("POST", base+"/plans/"+plan.Id.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	select {
	case <-created:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("agent did not execute command")
	}
	outage.Store(true)
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, e := j.Record(op.Id)
		if e == nil && r.Report.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local execution blocked on management")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
	j.Close()
	name := agent.Name(api.ExecutionCommand{ResourceId: plan.ResourceId})
	observed, e := docker.ContainerInspect(context.Background(), name, client.ContainerInspectOptions{})
	if e != nil || !observed.Container.State.Running {
		t.Fatal("management loss stopped probe", e)
	}
	id := observed.Container.ID
	defer func() {
		v, e := docker.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
		if e == nil && v.Container.Config.Labels["com.astilba.server"] == in.ServerId.String() {
			_, _ = docker.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
		}
	}()
	outage.Store(false)
	j, e = agent.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	agentClient = agent.NewClient(j, &agent.Engine{Journal: j, Docker: docker})
	agentClient.HTTP = tlsServer.Client()
	agentClient.HTTP.Timeout = 35 * time.Second
	agentClient.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done = make(chan error, 1)
	go func() { done <- agentClient.Run(ctx) }()
	deadline = time.Now().Add(10 * time.Second)
	for {
		var state string
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state)
		if state == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restarted agent did not report accepted result")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	after, e := docker.ContainerInspect(context.Background(), name, client.ContainerInspectOptions{})
	if e != nil || after.Container.ID != id {
		t.Fatal("restarted agent repeated external effect", e)
	}
}

func TestReenrollmentRecoversPendingRotation(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, _ := enrollment(t, f, b, ws, "build")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	j, e := agent.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	pending, _ := cryptobox.Token()
	_ = j.SetConfig(agent.Config{Origin: tlsServer.URL, ServerID: in.ServerId, MachineID: in.MachineId, Credential: in.Credential, Pending: pending})
	c := agent.NewClient(j, &agent.Engine{Journal: j})
	c.HTTP = tlsServer.Client()
	c.HTTP.Timeout = 35 * time.Second
	c.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
	}
	if e = c.Enroll(context.Background(), v.Token); e != nil {
		t.Fatal(e)
	}
	_, _ = f.st.Pool.Exec(context.Background(), "UPDATE servers SET last_seen=now()-interval '1 hour' WHERE id=$1", in.ServerId)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var current bool
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT last_seen>now()-interval '5 seconds' FROM servers WHERE id=$1", in.ServerId).Scan(&current)
		if current {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reenrolled agent did not recover heartbeat")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}
