// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed hosting_assets/*
var hostingAssets embed.FS

const hostingSchema = `
CREATE TABLE hosting_fixture (version integer PRIMARY KEY CHECK (version=1));
INSERT INTO hosting_fixture VALUES (1);
CREATE TABLE hosting_sessions (digest bytea PRIMARY KEY, csrf bytea NOT NULL, expires timestamptz NOT NULL);
CREATE TABLE hosting_operations (
 id text PRIMARY KEY, completed_at timestamptz, completions integer NOT NULL DEFAULT 0,
 completed_boot text NOT NULL DEFAULT ''
);`

type hostingConfig struct {
	database, origin, login, internal, agentSeed string
}

type hostingMetrics struct {
	started  time.Time
	received atomic.Int64
	sent     atomic.Int64
	requests atomic.Int64
}

type hostingConnection struct {
	net.Conn
	metrics *hostingMetrics
}

func (c *hostingConnection) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.metrics.received.Add(int64(n))
	return n, err
}

func (c *hostingConnection) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.metrics.sent.Add(int64(n))
	return n, err
}

type hostingJob struct {
	ID string `json:"id"`
}

func (hostingJob) Kind() string { return "plat6_fixture" }

type hostingWorker struct {
	river.WorkerDefaults[hostingJob]
	pool *pgxpool.Pool
	boot string
}

func (w *hostingWorker) Work(ctx context.Context, job *river.Job[hostingJob]) error {
	_, err := w.pool.Exec(ctx, `UPDATE hosting_operations SET completed_at=clock_timestamp(), completions=completions+1, completed_boot=$2
 WHERE id=$1 AND completed_at IS NULL`, job.Args.ID, w.boot)
	return err
}

func hostingAgentCredential(seed, id string) string {
	h := hmac.New(sha256.New, []byte(seed))
	h.Write([]byte(id))
	return hex.EncodeToString(h.Sum(nil))
}

// RunHostingFixture runs the disposable PLAT-6 server or initializes an empty
// database. The same entry point is used by ordinary Docker and Cloudflare.
func RunHostingFixture(ctx context.Context, initialize bool) error {
	cfg := hostingConfig{database: os.Getenv("PLAT6_DATABASE_URL"), origin: os.Getenv("PLAT6_PUBLIC_ORIGIN"),
		login: os.Getenv("PLAT6_LOGIN_TOKEN"), internal: os.Getenv("PLAT6_INTERNAL_TOKEN"), agentSeed: os.Getenv("PLAT6_AGENT_SEED")}
	u, err := url.Parse(cfg.origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("PLAT6_PUBLIC_ORIGIN must be an HTTPS origin without a path")
	}
	if cfg.database == "" || len(cfg.login) < 32 || len(cfg.internal) < 32 || len(cfg.agentSeed) < 32 {
		return errors.New("configure the database and three runtime secrets of at least 32 bytes")
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.database)
	if err != nil {
		return errors.New("invalid PLAT6_DATABASE_URL")
	}
	poolConfig.MaxConns = 16
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "plat6_fixture"
	metrics := &hostingMetrics{started: time.Now()}
	poolConfig.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &hostingConnection{Conn: conn, metrics: metrics}, nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("cannot open fixture database")
	}
	defer pool.Close()
	if initialize {
		if os.Getenv("PLAT6_DISPOSABLE_DATABASE") != "1" {
			return errors.New("initialization requires PLAT6_DISPOSABLE_DATABASE=1 and a dedicated empty database")
		}
		return initializeHosting(ctx, pool, cfg.agentSeed)
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT version FROM hosting_fixture`).Scan(&version); err != nil || version != 1 {
		return errors.New("initialize the dedicated fixture database before serving")
	}
	c, err := newHostingController(pool, cfg)
	if err != nil {
		return err
	}
	if err := c.queue.Start(ctx); err != nil {
		return errors.New("cannot start fixture workers")
	}
	c.metrics = metrics
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.queue.Stop(stop)
	}()
	s := &http.Server{Addr: ":8080", Handler: c, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.Shutdown(shutdown)
	}()
	err = s.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func initializeHosting(ctx context.Context, pool *pgxpool.Pool, seed string) error {
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&tables); err != nil {
		return errors.New("cannot inspect fixture database")
	}
	if tables != 0 {
		return errors.New("refusing to initialize a nonempty database")
	}
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return errors.New("fixture River migration failed")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, hostingSchema+transportSchema); err != nil {
		return err
	}
	for i := range 20 {
		id := fmt.Sprintf("server-%02d", i+1)
		if _, err := tx.Exec(ctx, `INSERT INTO transport_servers(id,role,current_hash) VALUES ($1,'deployment',$2)`,
			id, credentialHash(hostingAgentCredential(seed, id))); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type hostingController struct {
	pool      *pgxpool.Pool
	queue     *river.Client[pgx.Tx]
	transport *transportController
	cfg       hostingConfig
	boot      string
	metrics   *hostingMetrics
}

func newHostingController(pool *pgxpool.Pool, cfg hostingConfig) (*hostingController, error) {
	boot := newCredential()
	workers := river.NewWorkers()
	river.AddWorker(workers, &hostingWorker{pool: pool, boot: boot})
	queue, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 2}},
		JobTimeout: 10 * time.Second, FetchPollInterval: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, err
	}
	return &hostingController{pool: pool, queue: queue, cfg: cfg, boot: boot,
		metrics: &hostingMetrics{started: time.Now()}, transport: &transportController{pool: pool, polling: map[string]bool{}}}, nil
}

func secretMatches(actual, expected string) bool {
	a, b := sha256.Sum256([]byte(actual)), sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func (c *hostingController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.metrics.requests.Add(1)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		if c.pool.Ping(r.Context()) != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/_fixture/wake" && r.Method == http.MethodPost || r.URL.Path == "/_fixture/metrics" && r.Method == http.MethodGet {
		if !secretMatches(r.Header.Get("Authorization"), "Bearer "+c.cfg.internal) {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.URL.Path == "/_fixture/metrics" {
			var memory runtime.MemStats
			var usage syscall.Rusage
			runtime.ReadMemStats(&memory)
			if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
				http.Error(w, "metrics unavailable", 503)
				return
			}
			cpu := float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
			writeJSON(w, map[string]any{"boot": c.boot, "elapsed_seconds": time.Since(c.metrics.started).Seconds(),
				"cpu_seconds": cpu, "go_sys_bytes": memory.Sys, "requests": c.metrics.requests.Load(),
				"db_received_bytes": c.metrics.received.Load(), "db_sent_bytes": c.metrics.sent.Load()})
			return
		}
		w.WriteHeader(204)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		c.transport.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		c.api(w, r)
		return
	}
	if r.Method != http.MethodGet || (r.URL.Path != "/" && r.URL.Path != "/app.js") {
		http.NotFound(w, r)
		return
	}
	name, contentType := "index.html", "text/html; charset=utf-8"
	if r.URL.Path == "/app.js" {
		name, contentType = "app.js", "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	body, _ := hostingAssets.ReadFile("hosting_assets/" + name)
	w.Write(body)
}

func (c *hostingController) api(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); (origin != "" && origin != c.cfg.origin) ||
		(r.Method != http.MethodGet && origin != c.cfg.origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "invalid origin", 403)
		return
	}
	if r.URL.Path == "/api/login" && r.Method == http.MethodPost {
		var body struct{ Token string }
		if !decodeRequest(w, r, &body) {
			return
		}
		if !secretMatches(body.Token, c.cfg.login) {
			http.Error(w, "unauthorized", 401)
			return
		}
		session, csrf := newCredential(), newCredential()
		_, err := c.pool.Exec(r.Context(), `INSERT INTO hosting_sessions VALUES ($1,$2,clock_timestamp()+interval '2 hours')`, credentialHash(session), credentialHash(csrf))
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "__Host-plat6", Value: session, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 7200})
		writeJSON(w, map[string]string{"csrf": csrf})
		return
	}
	cookie, err := r.Cookie("__Host-plat6")
	if err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	var csrf []byte
	if err := c.pool.QueryRow(r.Context(), `SELECT csrf FROM hosting_sessions WHERE digest=$1 AND expires>clock_timestamp()`, credentialHash(cookie.Value)).Scan(&csrf); err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Method != http.MethodGet && subtle.ConstantTimeCompare(csrf, credentialHash(r.Header.Get("X-CSRF-Token"))) != 1 {
		http.Error(w, "invalid csrf token", 403)
		return
	}
	switch {
	case r.URL.Path == "/api/state" && r.Method == http.MethodGet:
		c.state(w, r)
	case r.URL.Path == "/api/events" && r.Method == http.MethodGet:
		c.events(w, r)
	case r.URL.Path == "/api/operations" && r.Method == http.MethodPost:
		c.enqueue(w, r)
	case r.URL.Path == "/api/logout" && r.Method == http.MethodPost:
		if _, err := c.pool.Exec(r.Context(), `DELETE FROM hosting_sessions WHERE digest=$1`, credentialHash(cookie.Value)); err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "__Host-plat6", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func (c *hostingController) state(w http.ResponseWriter, r *http.Request) {
	var complete, pending int
	err := c.pool.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE completed_at IS NOT NULL), count(*) FILTER (WHERE completed_at IS NULL) FROM hosting_operations`).Scan(&complete, &pending)
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	writeJSON(w, map[string]any{"boot": c.boot, "complete": complete, "pending": pending})
}

func (c *hostingController) enqueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID           string `json:"id"`
		DelaySeconds int    `json:"delay_seconds"`
	}
	if !decodeRequest(w, r, &body) {
		return
	}
	id, err := hex.DecodeString(body.ID)
	if err != nil || len(id) != 32 || body.DelaySeconds < 0 || body.DelaySeconds > 120 {
		http.Error(w, "invalid operation", 400)
		return
	}
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	defer tx.Rollback(context.Background())
	inserted, err := tx.Exec(r.Context(), `INSERT INTO hosting_operations(id) VALUES ($1) ON CONFLICT DO NOTHING`, body.ID)
	if err == nil && inserted.RowsAffected() == 1 {
		_, err = c.queue.InsertTx(r.Context(), tx, hostingJob{ID: body.ID}, &river.InsertOpts{ScheduledAt: time.Now().Add(time.Duration(body.DelaySeconds) * time.Second)})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	w.WriteHeader(202)
}

func (c *hostingController) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	controller := http.NewResponseController(w)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	end := time.NewTimer(40 * time.Second)
	defer end.Stop()
	for {
		var complete int
		if err := c.pool.QueryRow(r.Context(), `SELECT count(*) FROM hosting_operations WHERE completed_at IS NOT NULL`).Scan(&complete); err != nil {
			return
		}
		payload, _ := json.Marshal(map[string]any{"boot": c.boot, "complete": complete})
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", payload); err != nil || controller.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-end.C:
			return
		case <-tick.C:
		}
	}
}

// HostingAgentToken is only for the synthetic agents in this disposable spike.
func HostingAgentToken(seed string, index int) (string, string) {
	id := fmt.Sprintf("server-%02d", index)
	return id, hostingAgentCredential(seed, id)
}
