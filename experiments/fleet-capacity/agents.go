package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Cadences copied from services/platform/internal/agent/client.go. The generator
// never imports application code: it speaks the same public HTTP contract as an agent.
const (
	heartbeatEvery   = 15 * time.Second
	metricsEvery     = time.Minute
	reportEvery      = 2 * time.Second
	pollPause        = 250 * time.Millisecond
	metricsSendLimit = 5 * time.Second
	setupWorkers     = 8
)

type agentConfig struct {
	origin, publicOrigin, email, password, stats string
	agents, perWorkspace, services               int
	deploysPerDay                                float64
	deployDuration, ramp                         time.Duration
	settled                                      atomic.Bool // setup deployments are quick
}

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func token43() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// browser is the owner session used only for setup and to trigger deployments.
type browser struct {
	origin, public string
	password       string
	http           *http.Client
	mu             sync.Mutex
	cookies        map[string]string
	csrf           string
	// Reauthentication replaces the session token, so it waits for requests in flight.
	session sync.RWMutex
}

func (b *browser) call(ctx context.Context, method, path string, body, out any, want int) error {
	b.session.RLock()
	err := b.request(ctx, method, path, body, out, want)
	b.session.RUnlock()
	// The owner's recent-authentication window is short compared with a large setup.
	if err != nil && strings.Contains(err.Error(), "recent_auth_required") {
		if err = b.reauthenticate(ctx, b.password); err != nil {
			return err
		}
		b.session.RLock()
		defer b.session.RUnlock()
		return b.request(ctx, method, path, body, out, want)
	}
	return err
}

func (b *browser) reauthenticate(ctx context.Context, password string) error {
	b.session.Lock()
	defer b.session.Unlock()
	var s struct {
		CsrfToken string `json:"csrfToken"`
	}
	if err := b.request(ctx, "POST", "/api/v1/auth/reauthenticate", map[string]string{"password": password}, &s, 200); err != nil {
		return err
	}
	b.mu.Lock()
	b.csrf = s.CsrfToken
	b.mu.Unlock()
	return nil
}

func (b *browser) request(ctx context.Context, method, path string, body, out any, want int) error {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.origin+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Origin", b.public)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	b.mu.Lock()
	req.Header.Set("X-CSRF-Token", b.csrf)
	for k, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	b.mu.Unlock()
	res, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	b.mu.Lock()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	b.mu.Unlock()
	if res.StatusCode != want {
		return fmt.Errorf("%s %s: status %d want %d: %.300s", method, path, res.StatusCode, want, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (b *browser) login(ctx context.Context, email, password string) error {
	var s struct {
		CsrfToken string `json:"csrfToken"`
	}
	if err := b.call(ctx, "GET", "/api/v1/session", nil, &s, 200); err != nil {
		return err
	}
	b.csrf = s.CsrfToken
	if err := b.call(ctx, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, &s, 200); err != nil {
		return err
	}
	b.csrf = s.CsrfToken
	return nil
}

// Statistics are cumulative; the orchestrator differences snapshots around its window.
type endpointStats struct {
	Count     int64            `json:"count"`
	Status    map[string]int64 `json:"status"`
	LatencyUS map[string]int64 `json:"latencyLog2Micros"`
}

type statistics struct {
	mu        sync.Mutex
	Endpoints map[string]*endpointStats `json:"endpoints"`
	Deploys   struct {
		Started, Finished, Failed int64
		DeliveryMicros            map[string]int64
	} `json:"deploys"`
	Errors []string `json:"errors"`
	Ready  bool     `json:"ready"`
}

func (s *statistics) record(endpoint string, status int, d time.Duration) {
	bucket := fmt.Sprint(int(math.Ceil(math.Log2(float64(d.Microseconds() + 1)))))
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.Endpoints[endpoint]
	if e == nil {
		e = &endpointStats{Status: map[string]int64{}, LatencyUS: map[string]int64{}}
		s.Endpoints[endpoint] = e
	}
	e.Count++
	e.Status[fmt.Sprint(status)]++
	e.LatencyUS[bucket]++
}

// fail keeps the latest errors, so failures during a window survive any during setup.
func (s *statistics) fail(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Errors) == 200 {
		s.Errors = s.Errors[1:]
	}
	s.Errors = append(s.Errors, time.Now().UTC().Format(time.TimeOnly)+" "+fmt.Sprintf(format, args...))
}

func capabilities() map[string]any {
	// The extension set of an up-to-date, ready Ubuntu host against this controller.
	return map[string]any{"arch": "arm64", "os": "linux", "protocol": 1, "version": "v1.0.0-fleet", "docker": true,
		"backups": true, "build": true, "buildCache": true, "buildControls": true, "compose": true, "credentialFreeVisualCancel": true,
		"crossHost": false, "previews": true, "railpackVersion": "0.15.1", "replicas": true, "safeReleases": true, "schedules": true,
		"serviceOutputs": true, "troubleshooting": true, "upgrades": true, "visual": true}
}

func preflight() map[string]any {
	compose := "5.6.1"
	return map[string]any{"certificateContactConfigured": true, "composeVersion": &compose, "dockerVersion": "29.4.1", "existingContainers": 7,
		"freeBytes": int64(82) << 30, "notes": []string{"Ingress ports are occupied or unavailable. Existing workloads will not be adopted.", "Existing containers stay unmanaged."},
		"osRelease": "ubuntu 24.04", "port443Busy": true, "port80Busy": true, "ready": true}
}

type command struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Generation int64           `json:"generation"`
	PlanHash   string          `json:"planHash"`
	Visual     json.RawMessage `json:"visual"`
}

type pending struct {
	report       map[string]any
	acknowledged chan struct{}
}

type agent struct {
	id, credential string
	source         string
	workspace      string
	project, env   string
	unit           string
	revision       string
	apps           []string
	spec           map[string]any
	http           *http.Client
	cfg            *agentConfig
	stats          *statistics
	mu             sync.Mutex
	pending        map[string]*pending // the journal: reports not yet acknowledged
	seen           map[string]bool     // commands already executing
	delivered      chan string
	busy           atomic.Bool
}

func newAgent(cfg *agentConfig, st *statistics) *agent {
	// Mirrors NewClient: one client per host, so one TCP connection per concurrent call.
	return &agent{cfg: cfg, stats: st, credential: token43(), source: fmt.Sprintf("198.18.%d.%d", mrand.IntN(256), mrand.IntN(254)+1), pending: map[string]*pending{}, seen: map[string]bool{}, delivered: make(chan string, 8),
		http: &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 30 * time.Second, MaxConnsPerHost: 4}}}
}

func (a *agent) call(ctx context.Context, method, path, endpoint string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.origin+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	if endpoint != "enroll" {
		req.Header.Set("Authorization", "Bearer "+a.id+"."+a.credential)
	}
	req.Header.Set("Content-Type", "application/json")
	// Agents arrive from distinct addresses through a load balancer. One generator must
	// not share a client source, or enrollment hits its per-address budget.
	req.Header.Set("X-Forwarded-For", a.source)
	start := time.Now()
	res, err := a.http.Do(req)
	if err != nil {
		// Only shutdown is ignored: a send limit expiring is a failed request.
		if !errors.Is(ctx.Err(), context.Canceled) {
			a.stats.record(endpoint, 0, time.Since(start))
			a.stats.fail("%s: %v", endpoint, err)
		}
		return 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 524289))
	a.stats.record(endpoint, res.StatusCode, time.Since(start))
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// A held poll lease is an ordinary conflict when polls overlap.
		if !(endpoint == "poll" && res.StatusCode == 409) {
			a.stats.fail("%s: %d %.300s", endpoint, res.StatusCode, data)
		}
		return res.StatusCode, errors.New("rejected")
	}
	if out != nil {
		return res.StatusCode, json.Unmarshal(data, out)
	}
	return res.StatusCode, nil
}

func (a *agent) enroll(ctx context.Context, token string) error {
	_, err := a.call(ctx, "POST", "/agent/v1/enroll", "enroll", map[string]any{"serverId": a.id, "machineId": uuid4(), "credential": a.credential, "token": token, "capabilities": capabilities(), "preflight": preflight()}, nil)
	return err
}

func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (a *agent) metrics() map[string]any {
	cpu := 3 + mrand.Float64()*40
	services := []map[string]any{}
	a.mu.Lock()
	unit := a.unit
	a.mu.Unlock()
	for _, id := range a.apps {
		if unit == "" {
			break
		}
		services = append(services, map[string]any{"resourceId": id, "unitId": unit, "state": "running", "health": "healthy", "cpuPercent": mrand.Float64() * 30, "memoryUsedBytes": 200<<20 + mrand.Int64N(800<<20)})
	}
	return map[string]any{"partial": false, "services": services, "host": map[string]any{"diskSource": "docker", "cpuPercent": cpu,
		"memory": map[string]int64{"usedBytes": 3<<30 + mrand.Int64N(1<<30), "totalBytes": 8 << 30}, "disk": map[string]int64{"usedBytes": 40<<30 + mrand.Int64N(1<<30), "totalBytes": 160 << 30}}}
}

// run reproduces Client.Run's request pattern, randomly phased as a real fleet would be.
func (a *agent) run(ctx context.Context, ramp time.Duration) {
	if ramp > 0 && !wait(ctx, time.Duration(mrand.Int64N(int64(ramp)))) {
		return
	}
	var wg sync.WaitGroup
	periodic := func(every time.Duration, fn func()) {
		wg.Go(func() {
			if !wait(ctx, time.Duration(mrand.Int64N(int64(every)))) {
				return
			}
			for ctx.Err() == nil {
				fn()
				if !wait(ctx, every) {
					return
				}
			}
		})
	}
	periodic(heartbeatEvery, func() {
		_, _ = a.call(ctx, "POST", "/agent/v1/heartbeat", "heartbeat", map[string]any{"capabilities": capabilities(), "preflight": preflight()}, nil)
	})
	periodic(metricsEvery, func() {
		send, cancel := context.WithTimeout(ctx, metricsSendLimit)
		defer cancel()
		_, _ = a.call(send, "POST", "/agent/v1/metrics", "metrics", a.metrics(), nil)
	})
	periodic(reportEvery, func() { a.sendPending(ctx) })
	delay := time.Second
	for ctx.Err() == nil {
		var poll struct {
			Command *command `json:"command"`
		}
		_, err := a.call(ctx, "GET", "/agent/v1/poll", "poll", nil, &poll)
		if err == nil && poll.Command != nil {
			a.accept(ctx, *poll.Command)
		}
		if err != nil {
			if !wait(ctx, delay+time.Duration(mrand.IntN(500))*time.Millisecond) {
				break
			}
			delay = min(delay*2, 30*time.Second)
			continue
		}
		delay = time.Second
		if !wait(ctx, pollPause) {
			break
		}
	}
	wg.Wait()
}

// sendPending is the two-second report loop: every unacknowledged report is resent.
func (a *agent) sendPending(ctx context.Context) {
	a.mu.Lock()
	reports := make([]*pending, 0, len(a.pending))
	raw := make([][]byte, 0, len(a.pending))
	for _, p := range a.pending {
		b, _ := json.Marshal(p.report)
		reports = append(reports, p)
		raw = append(raw, b)
	}
	a.mu.Unlock()
	for i, p := range reports {
		if _, err := a.call(ctx, "POST", "/agent/v1/report", "report", json.RawMessage(raw[i]), nil); err != nil {
			continue
		}
		a.mu.Lock()
		if p.report["state"] != "running" && p.report["sequence"] == reportSeq(raw[i]) {
			delete(a.pending, p.report["id"].(string))
			close(p.acknowledged)
		}
		a.mu.Unlock()
	}
}

func reportSeq(raw []byte) any {
	var v struct {
		Sequence int64 `json:"sequence"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Sequence
}

func (a *agent) accept(ctx context.Context, c command) {
	a.mu.Lock()
	started := a.seen[c.ID]
	a.seen[c.ID] = true
	var p *pending
	if !started {
		// Journal before signalling, as the real agent does before it can report.
		p = &pending{acknowledged: make(chan struct{}), report: map[string]any{"id": c.ID, "generation": c.Generation, "planHash": c.PlanHash, "sequence": int64(1), "state": "running", "steps": []map[string]string{}, "note": ""}}
		a.pending[c.ID] = p
	}
	a.mu.Unlock()
	if started {
		return
	}
	select {
	case a.delivered <- c.ID:
	default:
	}
	go a.execute(ctx, c, p)
}

// execute stands in for the engine: journaled progress, then a terminal report whose
// observation holds the ten-line log tail the real agent collects per container.
func (a *agent) execute(ctx context.Context, c command, p *pending) {
	var payload struct {
		Action string `json:"action"`
		Plan   struct {
			Images        map[string]string `json:"images"`
			Specification struct {
				Applications []struct {
					ID     string `json:"id"`
					Source struct {
						Image string `json:"image"`
					} `json:"source"`
				} `json:"applications"`
			} `json:"specification"`
		} `json:"plan"`
	}
	if c.Kind != "visual" || json.Unmarshal(c.Visual, &payload) != nil {
		a.stats.fail("unexpected command %s", c.Kind)
		return
	}
	steps := []map[string]string{}
	update := func(mutate func()) {
		a.mu.Lock()
		defer a.mu.Unlock()
		mutate()
		p.report["sequence"] = p.report["sequence"].(int64) + 1
	}
	duration := 2 * time.Second
	if payload.Action == "deploy" && a.cfg.settled.Load() {
		duration = a.cfg.deployDuration
	}
	names := []string{"images", "networks", "volumes", "containers", "health", "ingress", "observe"}
	for _, name := range names {
		// The engine journals an intent before each effect and marks it done afterwards.
		update(func() {
			steps = append(steps, map[string]string{"name": name, "state": "intent", "detail": ""})
			p.report["steps"] = steps
		})
		if !wait(ctx, duration/time.Duration(len(names))) {
			return
		}
		update(func() {
			steps = append(steps[:len(steps)-1:len(steps)-1], map[string]string{"name": name, "state": "done", "detail": ""})
			p.report["steps"] = steps
		})
	}
	observation := map[string]any{"observedAt": time.Now().UTC().Format(time.RFC3339Nano), "stateHash": strings.Repeat("a", 64), "resourceDrift": false, "infrastructureDrift": false, "applied": false}
	resources := []map[string]any{}
	if payload.Action == "resolve" {
		images := map[string]string{}
		for _, app := range payload.Plan.Specification.Applications {
			digest := sha256.Sum256([]byte(app.Source.Image))
			images[app.ID] = app.Source.Image + "@sha256:" + hex.EncodeToString(digest[:])
		}
		observation["images"] = images
	} else {
		logLine := "2026-11-05T12:00:00.000000000Z level=info msg=\"request completed\" method=GET path=/healthz status=200 duration=1ms\n"
		for _, app := range payload.Plan.Specification.Applications {
			resources = append(resources, map[string]any{"id": app.ID, "kind": "service", "state": "running", "image": payload.Plan.Images[app.ID], "drift": false, "note": "", "logs": strings.Repeat(logLine, 10), "memoryBytes": 300 << 20})
		}
		observation["applied"] = true
	}
	observation["resources"] = resources
	raw, _ := json.Marshal(observation)
	update(func() {
		p.report["observation"] = json.RawMessage(raw)
		p.report["state"] = "succeeded"
	})
	select {
	case <-p.acknowledged:
	case <-ctx.Done():
	}
}

func applicationSpec(ids []string, tag, name string) map[string]any {
	apps := []map[string]any{}
	for i, id := range ids {
		app := map[string]any{"id": id, "name": fmt.Sprintf("Service %d", i+1), "source": map[string]any{"kind": "image", "image": "registry.example.test/fleet/app-" + fmt.Sprint(i) + ":" + tag}}
		if i == 0 {
			app["health"] = map[string]any{"port": 8080, "path": "/healthz"}
		}
		apps = append(apps, app)
	}
	spec := map[string]any{"version": 1, "variables": []any{}, "applications": apps}
	spec["routes"] = []map[string]any{{"id": uuid4(), "targetId": ids[0], "protocol": "http", "domain": name + ".fleet.example.test", "port": 8080, "listenPort": 0, "tls": "off", "allowCidrs": []string{}}}
	return spec
}

// deploy runs one real review cycle: new draft, plan, agent resolution, apply.
// deploy approves whatever host changes the plan needs reviewed, as the owner the harness
// signs in as; a plan that needs none ignores the approval.
func (a *agent) deploy(ctx context.Context, b *browser, tag string) error {
	path := "/api/v1/workspaces/" + a.workspace + "/projects/" + a.project + "/environments/" + a.env
	spec := applicationSpec(a.apps, tag, "s"+strings.ReplaceAll(a.id, "-", ""))
	if a.spec != nil {
		// Keep route identity stable so a redeployment changes images, not ingress.
		spec["routes"] = a.spec["routes"]
	}
	a.spec = spec
	var revision struct {
		ID string `json:"id"`
	}
	if err := b.call(ctx, "PUT", path+"/draft", map[string]any{"baseRevisionId": a.revision, "specification": spec}, &revision, 200); err != nil {
		return err
	}
	a.revision = revision.ID
	var status struct {
		ID   string `json:"id"`
		Plan *struct {
			UnitID string `json:"unitId"`
		} `json:"plan"`
	}
	if err := b.call(ctx, "POST", path+"/deployment-plans", map[string]any{"serverId": a.id, "baseRevisionId": a.revision, "acceptDrift": false}, &status, 201); err != nil {
		return err
	}
	for range 480 {
		if err := b.call(ctx, "GET", path+"/deployment-plans/"+status.ID, nil, &status, 200); err != nil {
			return err
		}
		if status.Plan != nil {
			break
		}
		if !wait(ctx, 500*time.Millisecond) {
			return ctx.Err()
		}
	}
	if status.Plan == nil {
		return errors.New("plan did not resolve")
	}
	applied := time.Now()
	for len(a.delivered) > 0 {
		<-a.delivered
	}
	if err := b.call(ctx, "POST", path+"/deployment-plans/"+status.ID+"/apply", map[string]any{"idempotencyKey": uuid4(), "approveHostChanges": true}, nil, 201); err != nil {
		return err
	}
	select {
	case <-a.delivered:
		bucket := fmt.Sprint(int(math.Ceil(math.Log2(float64(time.Since(applied).Microseconds() + 1)))))
		a.stats.mu.Lock()
		a.stats.Deploys.DeliveryMicros[bucket]++
		a.stats.mu.Unlock()
	case <-time.After(60 * time.Second):
		return errors.New("deploy command was not delivered")
	case <-ctx.Done():
		return ctx.Err()
	}
	// Terminal once the engine stand-in has been acknowledged.
	for ctx.Err() == nil {
		a.mu.Lock()
		open := len(a.pending)
		if open == 0 {
			a.unit = status.Plan.UnitID
		}
		a.mu.Unlock()
		if open == 0 {
			return nil
		}
		wait(ctx, time.Second)
	}
	return ctx.Err()
}

func runAgents(args []string) error {
	f := flag.NewFlagSet("agents", flag.ContinueOnError)
	cfg := agentConfig{}
	f.StringVar(&cfg.origin, "origin", "", "application URL")
	f.StringVar(&cfg.publicOrigin, "public-origin", "", "configured ASTILBA_ORIGIN")
	f.StringVar(&cfg.email, "email", "", "owner email")
	f.StringVar(&cfg.stats, "stats", ":9100", "statistics listener")
	f.IntVar(&cfg.agents, "agents", 100, "synthetic agents")
	f.IntVar(&cfg.perWorkspace, "per-workspace", 5, "servers per workspace")
	f.IntVar(&cfg.services, "services", 0, "deployed services per server; zero leaves servers idle and undeployed")
	f.Float64Var(&cfg.deploysPerDay, "deploys-per-day", 0, "deployment rate per server")
	f.DurationVar(&cfg.deployDuration, "deploy-duration", 2*time.Minute, "time a deployment stays in progress")
	f.DurationVar(&cfg.ramp, "ramp", 60*time.Second, "period over which agents start")
	if err := f.Parse(args); err != nil {
		return err
	}
	cfg.password = os.Getenv("FLEET_OWNER_PASSWORD")
	if cfg.origin == "" || cfg.email == "" || cfg.password == "" {
		return errors.New("--origin, --email and FLEET_OWNER_PASSWORD are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	st := &statistics{Endpoints: map[string]*endpointStats{}, Errors: []string{}}
	st.Deploys.DeliveryMicros = map[string]int64{}
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
			st.mu.Lock()
			defer st.mu.Unlock()
			_ = json.NewEncoder(w).Encode(st)
		})
		log.Print(http.ListenAndServe(cfg.stats, mux))
	}()
	b := &browser{origin: cfg.origin, public: cfg.publicOrigin, password: cfg.password, http: &http.Client{Timeout: 60 * time.Second}, cookies: map[string]string{}}
	if cfg.agents == 0 {
		st.mu.Lock()
		st.Ready = true
		st.mu.Unlock()
		<-ctx.Done()
		return nil
	}
	if err := b.login(ctx, cfg.email, cfg.password); err != nil {
		return err
	}
	agents, err := provision(ctx, b, &cfg, st)
	if err != nil {
		return err
	}
	log.Printf("provisioned %d agents", len(agents))
	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Go(func() { a.run(ctx, cfg.ramp) })
	}
	if cfg.services > 0 {
		if err = firstDeployments(ctx, b, &cfg, agents); err != nil {
			st.mu.Lock()
			defer st.mu.Unlock()
			return fmt.Errorf("%w; %d agent errors, first: %q", err, len(st.Errors), st.Errors[:min(3, len(st.Errors))])
		}
		log.Print("first deployments finished")
	}
	cfg.settled.Store(true)
	st.mu.Lock()
	st.Ready = true
	st.mu.Unlock()
	if cfg.deploysPerDay > 0 {
		wg.Go(func() { deployLoop(ctx, b, &cfg, st, agents) })
	}
	wg.Wait()
	return nil
}

func provision(ctx context.Context, b *browser, cfg *agentConfig, st *statistics) ([]*agent, error) {
	workspaces := (cfg.agents + cfg.perWorkspace - 1) / cfg.perWorkspace
	jobs := make(chan int)
	results := make(chan []*agent, workspaces)
	errs := make(chan error, setupWorkers)
	var wg sync.WaitGroup
	for range setupWorkers {
		wg.Go(func() {
			for index := range jobs {
				made, err := provisionWorkspace(ctx, b, cfg, st, index, min(cfg.perWorkspace, cfg.agents-index*cfg.perWorkspace))
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					return
				}
				results <- made
			}
		})
	}
	go func() {
		defer close(jobs)
		for i := range workspaces {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	agents := make([]*agent, 0, cfg.agents)
	for made := range results {
		agents = append(agents, made...)
	}
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	return agents, ctx.Err()
}

func provisionWorkspace(ctx context.Context, b *browser, cfg *agentConfig, st *statistics, index, count int) ([]*agent, error) {
	var ws struct {
		ID string `json:"id"`
	}
	if err := b.call(ctx, "POST", "/api/v1/workspaces", map[string]string{"name": fmt.Sprintf("Fleet %d", index)}, &ws, 201); err != nil {
		return nil, err
	}
	base := "/api/v1/workspaces/" + ws.ID
	var project struct {
		ID string `json:"id"`
	}
	if cfg.services > 0 {
		if err := b.call(ctx, "POST", base+"/projects", map[string]string{"name": "Fleet"}, &project, 201); err != nil {
			return nil, err
		}
	}
	out := []*agent{}
	for i := range count {
		var enrolled struct {
			Token  string `json:"token"`
			Server struct {
				ID string `json:"id"`
			} `json:"server"`
		}
		if err := b.call(ctx, "POST", base+"/servers", map[string]string{"name": fmt.Sprintf("Server %d", i), "role": "deployment"}, &enrolled, 201); err != nil {
			return nil, err
		}
		a := newAgent(cfg, st)
		a.id, a.workspace = enrolled.Server.ID, ws.ID
		if err := a.enroll(ctx, enrolled.Token); err != nil {
			st.mu.Lock()
			defer st.mu.Unlock()
			return nil, fmt.Errorf("enroll: %w: %q", err, st.Errors[:min(1, len(st.Errors))])
		}
		if cfg.services > 0 {
			var env struct {
				ID       string `json:"id"`
				Revision string `json:"draftRevisionId"`
			}
			if err := b.call(ctx, "POST", base+"/projects/"+project.ID+"/environments", map[string]string{"name": fmt.Sprintf("Environment %d", i)}, &env, 201); err != nil {
				return nil, err
			}
			a.project, a.env, a.revision = project.ID, env.ID, env.Revision
			for range cfg.services {
				a.apps = append(a.apps, uuid4())
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// firstDeployments gives every server real managed resources for its metric reports.
// Metric ownership checks and observation rows then run against production tables.
func firstDeployments(ctx context.Context, b *browser, cfg *agentConfig, agents []*agent) error {
	jobs := make(chan *agent)
	errs := make(chan error, setupWorkers*2)
	var done atomic.Int64
	var wg sync.WaitGroup
	for range setupWorkers * 2 {
		wg.Go(func() {
			for a := range jobs {
				err := a.deploy(ctx, b, "1.0.0")
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					return
				}
				if n := done.Add(1); n%100 == 0 {
					log.Printf("deployed %d", n)
				}
			}
		})
	}
	reauthenticated := time.Now()
	for _, a := range agents {
		if time.Since(reauthenticated) > 3*time.Minute {
			if err := b.reauthenticate(ctx, cfg.password); err != nil {
				return err
			}
			reauthenticated = time.Now()
		}
		select {
		case jobs <- a:
		case err := <-errs:
			close(jobs)
			return err
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

// deployLoop is a Poisson arrival process across the fleet at the requested daily rate.
func deployLoop(ctx context.Context, b *browser, cfg *agentConfig, st *statistics, agents []*agent) {
	perSecond := cfg.deploysPerDay * float64(len(agents)) / 86400
	var counter atomic.Int64
	var wg sync.WaitGroup
	for ctx.Err() == nil {
		if !wait(ctx, time.Duration(mrand.ExpFloat64()/perSecond*float64(time.Second))) {
			break
		}
		a := agents[mrand.IntN(len(agents))]
		if !a.busy.CompareAndSwap(false, true) {
			continue
		}
		st.mu.Lock()
		st.Deploys.Started++
		st.mu.Unlock()
		wg.Go(func() {
			defer a.busy.Store(false)
			err := a.deploy(ctx, b, fmt.Sprintf("1.0.%d", counter.Add(1)))
			if err != nil && ctx.Err() == nil {
				st.fail("deploy: %v", err)
			}
			st.mu.Lock()
			defer st.mu.Unlock()
			if err != nil {
				if ctx.Err() == nil {
					st.Deploys.Failed++
				}
				return
			}
			st.Deploys.Finished++
		})
	}
	wg.Wait()
}
