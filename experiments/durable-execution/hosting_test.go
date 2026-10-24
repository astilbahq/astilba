// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type hostingProbe struct {
	base                        string
	client                      *http.Client
	pool                        *pgxpool.Pool
	cookie                      *http.Cookie
	csrf, login, internal, seed string
}

func (p *hostingProbe) request(ctx context.Context, method, path, origin, csrf, bearer string, body any) (*http.Response, error) {
	var payload io.Reader
	if method != http.MethodGet && method != http.MethodHead {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, payload)
	if err != nil {
		return nil, err
	}
	if p.cookie != nil {
		req.AddCookie(p.cookie)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	return p.client.Do(req)
}

func (p *hostingProbe) check(t *testing.T, method, path, origin, csrf, bearer string, body any, status int) (*http.Response, []byte) {
	t.Helper()
	resp, err := p.request(t.Context(), method, path, origin, csrf, bearer, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s: got %d, want %d", method, path, resp.StatusCode, status)
	}
	for _, secret := range []string{p.login, p.internal, p.seed} {
		if strings.Contains(string(data), secret) {
			t.Fatal("response leaked a runtime secret")
		}
	}
	return resp, data
}

func (p *hostingProbe) signIn(t *testing.T) {
	t.Helper()
	resp, data := p.check(t, "POST", "/api/login", p.base, "", "", map[string]string{"token": p.login}, 200)
	var result struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(data, &result) != nil || result.CSRF == "" {
		t.Fatal("missing csrf token")
	}
	p.csrf = result.CSRF
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("login response is cacheable")
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "__Host-plat6" {
			if !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatal("unsafe session cookie")
			}
			p.cookie = cookie
		}
	}
	if p.cookie == nil {
		t.Fatal("missing session cookie")
	}
}

func (p *hostingProbe) state(t *testing.T) (string, int, int) {
	t.Helper()
	_, data := p.check(t, "GET", "/api/state", "", "", "", nil, 200)
	var state struct {
		Boot              string
		Complete, Pending int
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state.Boot, state.Complete, state.Pending
}

func TestManagementHosting(t *testing.T) {
	tokens := map[string]string{"PLAT6_LOGIN_TOKEN": newCredential(), "PLAT6_INTERNAL_TOKEN": newCredential(), "PLAT6_AGENT_SEED": newCredential()}
	h := newHTTPSHarness(t, tokens)
	t.Cleanup(func() {
		if t.Failed() {
			rows, err := h.pool.Query(context.Background(), `SELECT id,state,attempt,scheduled_at,errors FROM river_job`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					values, _ := rows.Values()
					t.Logf("job state: %v", values)
				}
			}
		}
	})
	p := &hostingProbe{base: h.url, client: h.client, pool: h.pool, login: tokens["PLAT6_LOGIN_TOKEN"], internal: tokens["PLAT6_INTERNAL_TOKEN"], seed: tokens["PLAT6_AGENT_SEED"]}
	verifyManagementHosting(t, p, h.replaceController)
	var wrong int
	if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM hosting_operations WHERE completions <> 1`).Scan(&wrong); err != nil || wrong != 0 {
		t.Fatalf("operations did not complete exactly once: count=%d err=%v", wrong, err)
	}
}

func verifyManagementHosting(t *testing.T, p *hostingProbe, restart func()) {
	t.Helper()
	p.check(t, "GET", "/", "", "", "", nil, 200)
	p.check(t, "GET", "/api/state", "", "", "", nil, 401)
	for _, origin := range []string{"", "https://attacker.invalid"} {
		p.check(t, "POST", "/api/login", origin, "", "", map[string]string{"token": p.login}, 403)
	}
	p.check(t, "POST", "/api/login", p.base, "", "", map[string]string{"token": "wrong"}, 401)
	p.check(t, "GET", "/_fixture/metrics", "", "", "wrong", nil, 401)
	p.signIn(t)
	boot, before, _ := p.state(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	resp, err := p.request(ctx, "GET", "/api/events", "", "", "", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		resp.Body.Close()
		t.Fatal("missing SSE stream")
	}
	reader := bufio.NewReader(resp.Body)
	started := time.Now()
	for count := 0; count < 2; {
		line, err := reader.ReadString('\n')
		if err != nil {
			cancel()
			resp.Body.Close()
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data:") {
			count++
		}
	}
	if elapsed := time.Since(started); elapsed < 750*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("SSE was buffered or not incremental: %s", elapsed)
	}
	resp.Body.Close()
	cancel()

	id := newCredential()
	body := map[string]any{"id": id, "delay_seconds": 5}
	p.check(t, "POST", "/api/operations", p.base, "", "", body, 403)
	p.check(t, "POST", "/api/operations", "https://attacker.invalid", p.csrf, "", body, 403)
	p.check(t, "POST", "/api/operations", p.base, p.csrf, "", body, 202)
	p.check(t, "POST", "/api/operations", p.base, p.csrf, "", body, 202)
	_, _, pending := p.state(t)
	if pending < 1 {
		t.Fatal("accepted operation not durably visible")
	}
	restart()
	wait(t, 30*time.Second, func() bool {
		resp, err := p.request(t.Context(), "GET", "/health", "", "", "", nil)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 204
	})
	after, _, _ := p.state(t)
	if after == boot {
		t.Fatal("controller was not replaced")
	}
	wait(t, 90*time.Second, func() bool { _, complete, _ := p.state(t); return complete == before+1 })
	var completedBoot string
	var completions int
	if err := p.pool.QueryRow(t.Context(), `SELECT completed_boot,completions FROM hosting_operations WHERE id=$1`, id).Scan(&completedBoot, &completions); err != nil {
		t.Fatal(err)
	}
	if completedBoot != after || completions != 1 {
		t.Fatal("operation was not completed exactly once by the replacement process")
	}

	id, credential := HostingAgentToken(p.seed, 1)
	pollPath := fmt.Sprintf("/v1/servers/%s/poll", id)
	type pollResult struct {
		status  int
		err     error
		elapsed time.Duration
	}
	finished := make(chan pollResult, 1)
	go func() {
		started := time.Now()
		resp, err := p.request(t.Context(), "POST", pollPath, "", "", credential, map[string]any{})
		result := pollResult{err: err, elapsed: time.Since(started)}
		if resp != nil {
			result.status = resp.StatusCode
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		finished <- result
	}()
	time.Sleep(time.Second)
	p.check(t, "POST", fmt.Sprintf("/v1/servers/%s/heartbeat", id), "", "", credential, map[string]any{}, 204)
	p.check(t, "POST", fmt.Sprintf("/v1/servers/%s/heartbeat", id), "", "", "wrong", map[string]any{}, 401)
	result := <-finished
	if result.err != nil || result.status != 204 || result.elapsed < 24*time.Second || result.elapsed > 35*time.Second {
		t.Fatalf("long poll: status=%d elapsed=%s err=%v", result.status, result.elapsed, result.err)
	}
	p.check(t, "POST", "/api/logout", p.base, p.csrf, "", map[string]any{}, 204)
	p.check(t, "GET", "/api/state", "", "", "", nil, 401)
	t.Log("cookie/CSRF denials, incremental SSE, replacement recovery, idempotent job and concurrent 25s poll/heartbeat passed")
}

func remoteHostingProbe(t *testing.T) *hostingProbe {
	t.Helper()
	base := os.Getenv("PLAT6_LIVE_URL")
	if base == "" {
		t.Skip("set PLAT6_LIVE_URL and runtime secrets for live Cloudflare checks")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("PLAT6_LIVE_URL must be an HTTPS origin")
	}
	p := &hostingProbe{base: base, client: &http.Client{Timeout: 35 * time.Second}, login: os.Getenv("PLAT6_LOGIN_TOKEN"), internal: os.Getenv("PLAT6_INTERNAL_TOKEN"), seed: os.Getenv("PLAT6_AGENT_SEED")}
	if len(p.login) < 32 || len(p.internal) < 32 || len(p.seed) < 32 {
		t.Fatal("configure all three fixture runtime secrets")
	}
	if os.Getenv("PLAT6_DATABASE_URL") == "" {
		t.Fatal("configure the dedicated test database URL")
	}
	p.pool, err = pgxpool.New(t.Context(), os.Getenv("PLAT6_DATABASE_URL"))
	if err != nil {
		t.Fatal("cannot open the dedicated test database")
	}
	t.Cleanup(p.pool.Close)
	return p
}

func TestCloudflareHosting(t *testing.T) {
	p := remoteHostingProbe(t)
	verifyManagementHosting(t, p, func() { p.check(t, "POST", "/_fixture/stop", "", "", p.internal, nil, 204) })
}

func TestCloudflareWake(t *testing.T) {
	p := remoteHostingProbe(t)
	pool := p.pool
	var version int
	if pool.QueryRow(t.Context(), `SELECT version FROM hosting_fixture`).Scan(&version) != nil || version != 1 {
		t.Fatal("not an initialized PLAT-6 database")
	}
	p.signIn(t)
	// Leave enough time between minute cron ticks to observe natural sleep.
	next := time.Now().Truncate(time.Minute).Add(time.Minute + 5*time.Second)
	time.Sleep(time.Until(next))
	_, before, _ := p.state(t)
	id := newCredential()
	p.check(t, "POST", "/api/operations", p.base, p.csrf, "", map[string]any{"id": id, "delay_seconds": 45}, 202)
	time.Sleep(40 * time.Second)
	_, data := p.check(t, "GET", "/_fixture/status", "", "", p.internal, nil, 200)
	var status struct{ Running bool }
	if json.Unmarshal(data, &status) != nil || status.Running {
		t.Fatal("container did not naturally sleep without browser/agent traffic")
	}
	started := time.Now()
	wait(t, 90*time.Second, func() bool {
		var completed bool
		return pool.QueryRow(t.Context(), `SELECT completed_at IS NOT NULL FROM hosting_operations WHERE id=$1`, id).Scan(&completed) == nil && completed
	})
	// Only PostgreSQL was observed while the Cron Trigger woke the container.
	_, after, _ := p.state(t)
	if after != before+1 {
		t.Fatal("scheduled work did not complete exactly once after wake")
	}
	t.Logf("natural sleep and scheduled wake passed; completion observed %s after sleep check", time.Since(started))
}

type hostingSample struct {
	Boot       string  `json:"boot"`
	Elapsed    float64 `json:"elapsed_seconds"`
	CPU        float64 `json:"cpu_seconds"`
	GoSys      int64   `json:"go_sys_bytes"`
	Requests   int64   `json:"requests"`
	DBReceived int64   `json:"db_received_bytes"`
	DBSent     int64   `json:"db_sent_bytes"`
}

func (p *hostingProbe) metrics(t *testing.T) hostingSample {
	t.Helper()
	_, data := p.check(t, "GET", "/_fixture/metrics", "", "", p.internal, nil, 200)
	var sample hostingSample
	if err := json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	return sample
}

func TestCloudflareLoad(t *testing.T) {
	p := remoteHostingProbe(t)
	for _, count := range []int{0, 1, 5, 20} {
		t.Run(fmt.Sprintf("%d_agents", count), func(t *testing.T) {
			before := p.metrics(t)
			started := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			var workers sync.WaitGroup
			var successes, failures atomic.Int64
			for agent := 1; agent <= count; agent++ {
				id, credential := HostingAgentToken(p.seed, agent)
				for _, action := range []string{"poll", "heartbeat"} {
					workers.Go(func() {
						for ctx.Err() == nil {
							// Stop starting requests at the sample deadline, but drain
							// existing polls before the next group reuses identities.
							resp, err := p.request(t.Context(), "POST", "/v1/servers/"+id+"/"+action, "", "", credential, map[string]any{})
							if err != nil {
								failures.Add(1)
								var networkError net.Error
								t.Logf("%s transport failure: timeout=%t", action, errors.As(err, &networkError) && networkError.Timeout())
								return
							}
							io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
							if resp.StatusCode != 204 {
								failures.Add(1)
								t.Logf("%s response status: %d", action, resp.StatusCode)
								return
							}
							successes.Add(1)
							if action == "heartbeat" {
								select {
								case <-ctx.Done():
									return
								case <-time.After(15 * time.Second):
								}
							}
						}
					})
				}
			}
			<-ctx.Done()
			workers.Wait()
			after := p.metrics(t)
			changed := before.Boot != after.Boot
			if failures.Load() != 0 || count > 0 && changed {
				t.Fatalf("load failed: errors=%d boot_changed=%t", failures.Load(), changed)
			}
			measurement := map[string]any{"agents": count, "wall_seconds": time.Since(started).Seconds(), "successes": successes.Load(), "errors": failures.Load(), "boot_changed": changed,
				"before": before, "after": after}
			data, _ := json.Marshal(measurement)
			t.Logf("PLAT6_MEASUREMENT %s", data)
		})
	}
}
