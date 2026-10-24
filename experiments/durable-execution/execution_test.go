// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const postgresImage = "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e"
const effectImage = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

type processConfig struct {
	Role, DB, Socket, Journal, Docker, Marker, Pause string
	LongLease                                        bool
}

// The parent launches the compiled test binary as two real OS processes. Kill
// tests therefore lose all in-memory state rather than simulating an error.
func TestSubprocess(t *testing.T) {
	path := os.Getenv("PLAT2_PROCESS_CONFIG")
	if path == "" {
		t.Skip("subprocess entry point")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg processConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	point := func(name string) {
		if name != cfg.Pause {
			return
		}
		if err := os.WriteFile(cfg.Marker, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		wait(t, 2*time.Minute, func() bool { _, err := os.Stat(cfg.Marker + ".release"); return err == nil })
	}
	if cfg.Role == "agent" {
		a, err := openAgent(cfg.Journal, cfg.Docker, effectImage, point)
		if err != nil {
			t.Fatal(err)
		}
		defer a.close()
		// Only this private test directory is touched, never an arbitrary socket.
		os.Remove(cfg.Socket)
		listener, err := net.Listen("unix", cfg.Socket)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(cfg.Socket, 0o600); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(path+".ready", nil, 0o600)
		server := &http.Server{Handler: a, ReadHeaderTimeout: time.Second}
		if err := server.Serve(listener); err != nil {
			t.Fatal(err)
		}
	} else {
		pool, err := pgxpool.New(context.Background(), cfg.DB)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		q, err := newQueue(pool, cfg.Socket, point)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.LongLease {
			workers := river.NewWorkers()
			river.AddWorker(workers, &worker{pool: pool, socket: cfg.Socket, point: point})
			q, err = river.NewClient(riverpgxv5.New(pool), &river.Config{
				Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
				JobTimeout: 5 * time.Minute, RescueStuckJobsAfter: 6 * time.Minute, RetryPolicy: quickRetry{},
				Logger: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})),
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := q.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(path+".ready", nil, 0o600)
		select {}
	}
}

func wait(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not satisfied within %s", timeout)
}

func token() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type process struct {
	cmd         *exec.Cmd
	log, marker string
}

func startProcess(t *testing.T, cfg processConfig) *process {
	t.Helper()
	path := filepath.Join(t.TempDir(), cfg.Role+".json")
	cfg.Marker = path + ".barrier"
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(path + ".log")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSubprocess$", "-test.v")
	cmd.Env = append(os.Environ(), "PLAT2_PROCESS_CONFIG="+path)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &process{cmd: cmd, log: path + ".log", marker: cfg.Marker}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(p.log)
			t.Logf("%s process: %s", cfg.Role, data)
		}
	})
	wait(t, 15*time.Second, func() bool { _, err := os.Stat(path + ".ready"); return err == nil })
	return p
}

func (p *process) barrier(t *testing.T) {
	t.Helper()
	wait(t, 10*time.Second, func() bool { _, err := os.Stat(p.marker); return err == nil })
}

type harness struct {
	docker            *client.Client
	host, run, baseDB string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	host := os.Getenv("PLAT2_DOCKER_HOST")
	if host == "" {
		t.Skip("set PLAT2_DOCKER_HOST to a disposable local Docker Unix socket to run crash integration tests")
	}
	if !strings.HasPrefix(host, "unix://") {
		t.Fatal("this fixture only accepts an explicit local Unix Docker socket")
	}
	docker, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{docker: docker, host: host, run: token()}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		items, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", ownerLabel+"="+h.run)})
		if err != nil {
			t.Errorf("cleanup inventory: %v", err)
			return
		}
		for _, c := range items.Items {
			i, err := docker.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
			if err != nil || i.Container.Config.Labels[ownerLabel] != h.run {
				t.Errorf("cannot verify ownership of %s", c.ID)
				continue
			}
			if _, err := docker.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
		docker.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, image := range []string{postgresImage, effectImage} {
		if _, err := docker.ImageInspect(ctx, image); err != nil {
			t.Fatalf("pull pinned fixture image first: %s: %v", image, err)
		}
	}
	c, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "plat2-postgres-" + h.run, Image: postgresImage,
		Config:     &container.Config{Env: []string{"POSTGRES_HOST_AUTH_METHOD=trust"}, Labels: map[string]string{ownerLabel: h.run}, ExposedPorts: network.PortSet{network.MustParsePort("5432/tcp"): {}}},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{network.MustParsePort("5432/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1")}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerStart(ctx, c.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	i, err := docker.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	port := i.Container.NetworkSettings.Ports[network.MustParsePort("5432/tcp")][0].HostPort
	h.baseDB = "postgres://postgres@127.0.0.1:" + port + "/postgres?sslmode=disable"
	wait(t, 20*time.Second, func() bool {
		c, err := pgx.Connect(context.Background(), h.baseDB)
		if err != nil {
			return false
		}
		defer c.Close(context.Background())
		return c.Ping(context.Background()) == nil
	})
	version, err := docker.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run=%s Docker=%s API=%s host platform=%s/%s", h.run, version.Version, version.APIVersion, version.Os, version.Arch)
	return h
}

type fixture struct {
	h     *harness
	pool  *pgxpool.Pool
	queue *river.Client[pgx.Tx]
	cfg   processConfig
	c     command
}

func (h *harness) fixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, h.baseDB)
	if err != nil {
		t.Fatal(err)
	}
	dbName := "plat2_" + token()
	if _, err := c.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)
	u, _ := url.Parse(h.baseDB)
	u.Path = "/" + dbName
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// macOS Unix socket paths have a short limit; keep the socket outside the
	// long test directory, but in its own private disposable directory.
	socketDir, err := os.MkdirTemp("", "p2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	cfg := processConfig{DB: u.String(), Socket: filepath.Join(socketDir, "a.sock"), Journal: filepath.Join(dir, "journal.db"), Docker: h.host}
	q, err := newQueue(pool, cfg.Socket, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h: h, pool: pool, queue: q, cfg: cfg, c: command{Run: h.run, ID: token(), Environment: token(), Generation: 1, PlanHash: "frozen-plan", Fence: 1}}
}

func (f *fixture) agent(t *testing.T, pause string) *process {
	cfg := f.cfg
	cfg.Role = "agent"
	cfg.Pause = pause
	return startProcess(t, cfg)
}
func (f *fixture) worker(t *testing.T, pause string, long bool) *process {
	cfg := f.cfg
	cfg.Role = "worker"
	cfg.Pause = pause
	cfg.LongLease = long
	return startProcess(t, cfg)
}
func (f *fixture) enqueue(t *testing.T) int64 {
	t.Helper()
	id, err := enqueue(context.Background(), f.pool, f.queue, f.c, true)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *fixture) age(t *testing.T, id int64) {
	t.Helper()
	wait(t, 10*time.Second, func() bool {
		var old bool
		err := f.pool.QueryRow(context.Background(), "SELECT attempted_at < now()-interval '5 seconds' FROM river_job WHERE id=$1", id).Scan(&old)
		return err == nil && old
	})
}
func (f *fixture) done(t *testing.T, id int64, state string) {
	t.Helper()
	wait(t, 75*time.Second, func() bool {
		var s string
		err := f.pool.QueryRow(context.Background(), "SELECT state FROM river_job WHERE id=$1", id).Scan(&s)
		return err == nil && s == "completed"
	})
	var got string
	var observations, finalizations int
	err := f.pool.QueryRow(context.Background(), "SELECT state,observations,finalizations FROM operations WHERE id=$1", f.c.ID).Scan(&got, &observations, &finalizations)
	if err != nil {
		t.Fatal(err)
	}
	if got != state || observations != 1 || finalizations != 1 {
		t.Fatalf("operation = %s observations=%d finalizations=%d; want %s/1/1", got, observations, finalizations, state)
	}
	t.Logf("operation=%s state=%s observations=%d finalizations=%d", f.c.ID, got, observations, finalizations)
}

func (f *fixture) originalEffect(t *testing.T, id string) string {
	t.Helper()
	i, err := f.h.docker.ContainerInspect(context.Background(), f.c.name(), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if id != "" && i.Container.ID != id {
		t.Fatalf("effect replaced: %s -> %s", id, i.Container.ID)
	}
	if i.Container.RestartCount != 0 {
		t.Fatalf("container restarted %d times", i.Container.RestartCount)
	}
	wait(t, 5*time.Second, func() bool {
		logs, err := f.h.docker.ContainerLogs(context.Background(), i.Container.ID, client.ContainerLogsOptions{ShowStdout: true})
		if err != nil {
			return false
		}
		defer logs.Close()
		var out bytes.Buffer
		_, err = stdcopy.StdCopy(&out, &out, logs)
		if err != nil {
			return false
		}
		if strings.Count(out.String(), "effect\n") > 1 {
			t.Fatalf("effect executed more than once: %q", out.String())
		}
		return out.String() == "effect\n"
	})
	return i.Container.ID
}

func TestDurableExecution(t *testing.T) {
	h := newHarness(t)
	t.Run("transactional_enqueue", func(t *testing.T) {
		f := h.fixture(t)
		if _, err := enqueue(context.Background(), f.pool, f.queue, f.c, false); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"operations", "river_job"} {
			var n int
			if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
				t.Fatalf("rollback %s: count=%d err=%v", table, n, err)
			}
		}
		f.enqueue(t)
		for _, table := range []string{"operations", "river_job"} {
			var n int
			if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Fatalf("commit %s: count=%d err=%v", table, n, err)
			}
		}
	})
	for _, tc := range []struct{ point, state string }{{"accepted", "succeeded"}, {"intent", "needs_attention"}, {"created", "needs_attention"}, {"effect", "succeeded"}, {"recorded", "succeeded"}} {
		t.Run("agent_crash_"+tc.point, func(t *testing.T) {
			f := h.fixture(t)
			a := f.agent(t, tc.point)
			id := f.enqueue(t)
			w := f.worker(t, "", false)
			a.barrier(t)
			var original string
			if tc.point == "effect" || tc.point == "recorded" {
				original = f.originalEffect(t, "")
			}
			if err := w.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := a.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			// Wait for the killed process to release bbolt's file lock.
			a.cmd.Wait()
			f.agent(t, "")
			f.age(t, id)
			f.worker(t, "", false)
			f.done(t, id, tc.state)
			if original != "" {
				f.originalEffect(t, original)
			}
			if tc.state == "needs_attention" {
				newer := f.c
				newer.ID = token()
				newer.Generation++
				if _, err := send(context.Background(), f.cfg.Socket, newer); err == nil {
					t.Fatal("unresolved operation admitted a conflicting generation")
				}
				var note string
				f.pool.QueryRow(context.Background(), "SELECT note FROM operations WHERE id=$1", f.c.ID).Scan(&note)
				if note == "" {
					t.Fatal("needs_attention has no actionable diagnosis")
				}
			}
		})
	}
	for _, point := range []string{"checkpoint_uncommitted", "checkpoint_committed"} {
		t.Run(point, func(t *testing.T) {
			f := h.fixture(t)
			f.agent(t, "")
			id := f.enqueue(t)
			w := f.worker(t, point, false)
			w.barrier(t)
			original := f.originalEffect(t, "")
			if err := w.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			w.cmd.Wait()
			var observations int
			var checkpoint string
			if err := f.pool.QueryRow(context.Background(), "SELECT observations FROM operations WHERE id=$1", f.c.ID).Scan(&observations); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(context.Background(), "SELECT checkpoint FROM operations WHERE id=$1", f.c.ID).Scan(&checkpoint); err != nil {
				t.Fatal(err)
			}
			committed := point == "checkpoint_committed"
			if (observations == 1) != committed || (checkpoint == "observed") != committed {
				t.Fatalf("checkpoint mismatch: observations=%d checkpoint=%s", observations, checkpoint)
			}
			f.age(t, id)
			f.worker(t, "", false)
			f.done(t, id, "succeeded")
			f.originalEffect(t, original)
		})
	}
	for _, point := range []string{"completion_uncommitted", "completion_committed"} {
		t.Run(point, func(t *testing.T) {
			f := h.fixture(t)
			f.agent(t, "")
			id := f.enqueue(t)
			w := f.worker(t, point, false)
			w.barrier(t)
			if err := w.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			w.cmd.Wait()
			var finalizations int
			var state string
			if err := f.pool.QueryRow(context.Background(), "SELECT finalizations FROM operations WHERE id=$1", f.c.ID).Scan(&finalizations); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(context.Background(), "SELECT state FROM river_job WHERE id=$1", id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			committed := point == "completion_committed"
			if (finalizations == 1) != committed || (state == "completed") != committed {
				t.Fatalf("completion transaction split: finalizations=%d job=%s", finalizations, state)
			}
			if !committed {
				f.age(t, id)
				f.worker(t, "", false)
			}
			f.done(t, id, "succeeded")
			f.originalEffect(t, "")
		})
	}
	t.Run("duplicate_delivery_and_generation_fencing", func(t *testing.T) {
		f := h.fixture(t)
		f.agent(t, "")
		for range 5 {
			r, err := send(context.Background(), f.cfg.Socket, f.c)
			if err != nil || r.State != "succeeded" {
				t.Fatalf("duplicate: %+v %v", r, err)
			}
		}
		original := f.originalEffect(t, "")
		mutated := f.c
		mutated.PlanHash = "different-plan"
		if _, err := send(context.Background(), f.cfg.Socket, mutated); err == nil {
			t.Fatal("accepted changed plan under same operation ID")
		}
		newer := f.c
		newer.ID = token()
		newer.Generation++
		newer.Fence = 2
		if _, err := send(context.Background(), f.cfg.Socket, newer); err != nil {
			t.Fatal(err)
		}
		stale := f.c
		stale.ID = token()
		if _, err := send(context.Background(), f.cfg.Socket, stale); err == nil {
			t.Fatal("accepted stale generation")
		}
		if _, err := send(context.Background(), f.cfg.Socket, f.c); err != nil {
			t.Fatal(err)
		}
		stale = newer
		stale.Fence = 1
		if _, err := send(context.Background(), f.cfg.Socket, stale); err == nil {
			t.Fatal("accepted stale delivery fence")
		}
		f.originalEffect(t, original)
	})
	t.Run("checkpoint_connection_loss", func(t *testing.T) {
		f := h.fixture(t)
		f.agent(t, "")
		id := f.enqueue(t)
		w := f.worker(t, "checkpoint_uncommitted", false)
		w.barrier(t)
		rows, err := f.pool.Query(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
 WHERE datname=current_database() AND state='idle in transaction' AND pid<>pg_backend_pid()`)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			var killed bool
			if err := rows.Scan(&killed); err != nil {
				t.Fatal(err)
			}
			if killed {
				count++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("terminated %d checkpoint connections, want 1", count)
		}
		// Release only this attempt; subsequent retries pass this marker.
		if err := os.WriteFile(w.marker+".release", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		f.done(t, id, "succeeded")
		f.originalEffect(t, "")
	})
	t.Run("cancellation_before_effect", func(t *testing.T) {
		f := h.fixture(t)
		f.c.Cancel = true
		f.agent(t, "")
		id := f.enqueue(t)
		f.worker(t, "", false)
		f.done(t, id, "cancelled")
		if _, err := h.docker.ContainerInspect(context.Background(), f.c.name(), client.ContainerInspectOptions{}); err == nil {
			t.Fatal("cancelled operation created a container")
		}
	})
	for _, cancellation := range []struct{ point, state string }{
		{"effect", "cancelled"}, {"recorded", "succeeded"}, {"checkpoint_committed", "succeeded"},
	} {
		t.Run("cancellation_after_"+cancellation.point, func(t *testing.T) {
			f := h.fixture(t)
			agentPoint, workerPoint := cancellation.point, ""
			if cancellation.point == "checkpoint_committed" {
				agentPoint, workerPoint = "", cancellation.point
			}
			a := f.agent(t, agentPoint)
			oldID := f.enqueue(t)
			w := f.worker(t, workerPoint, false)
			if agentPoint != "" {
				a.barrier(t)
			} else {
				w.barrier(t)
			}
			original := f.originalEffect(t, "")
			w.cmd.Process.Kill()
			a.cmd.Process.Kill()
			a.cmd.Wait()
			tx, err := f.pool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(context.Background(), "UPDATE operations SET cancel_requested=true WHERE id=$1", f.c.ID); err != nil {
				t.Fatal(err)
			}
			// Cancelling a queue job alone cannot undo or reconcile its external
			// effect. Persist cancellation and a reconciliation delivery atomically.
			if _, err := f.queue.JobCancelTx(context.Background(), tx, oldID); err != nil {
				t.Fatal(err)
			}
			inserted, err := f.queue.InsertTx(context.Background(), tx, jobArgs{ID: f.c.ID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.agent(t, "")
			f.worker(t, "", false)
			f.done(t, inserted.Job.ID, cancellation.state)
			var requested bool
			if err := f.pool.QueryRow(context.Background(), "SELECT cancel_requested FROM operations WHERE id=$1", f.c.ID).Scan(&requested); err != nil || !requested {
				t.Fatalf("cancellation request was lost: %t %v", requested, err)
			}
			f.originalEffect(t, original)
		})
	}
	t.Run("live_stale_worker_after_rescue", func(t *testing.T) {
		f := h.fixture(t)
		f.agent(t, "")
		id := f.enqueue(t)
		old := f.worker(t, "response", true)
		old.barrier(t)
		original := f.originalEffect(t, "")
		if err := old.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { old.cmd.Process.Signal(syscall.SIGCONT) })
		f.age(t, id)
		f.worker(t, "", false)
		f.done(t, id, "succeeded")
		if err := os.WriteFile(old.marker+".release", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := old.cmd.Process.Signal(syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		wait(t, 10*time.Second, func() bool {
			data, _ := os.ReadFile(old.log)
			return strings.Contains(string(data), "lost operation fence")
		})
		f.done(t, id, "succeeded")
		f.originalEffect(t, original)
	})
}

func TestJournalExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	a, err := openAgent(path, "unix:///unused", effectImage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if other, err := openAgent(path, "unix:///unused", effectImage, func(string) {}); err == nil {
		other.close()
		t.Fatal("two agents opened the same journal")
	}
}
