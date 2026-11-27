package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// downloading is Docker's report that it has received current bytes of layer id, of total.
func downloading(id string, current, total int64) dockerPullMessage {
	m := dockerPullMessage{ID: id, Status: "Downloading"}
	m.Progress = &struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	}{current, total}
	return m
}

// Docker's pull ends as stalled once its reports show the registry fell behind the floor by more
// than Docker can hold unreported: not while a link keeps up with the floor in Docker's steps, nor
// while Docker unpacks what it has, and not on what a fast window sent before. Time Docker waits
// on what it does not report, such as the image's config, counts.
func TestDockerPullWatch(t *testing.T) {
	const window = 100 * time.Millisecond
	const total = 64 << 20
	for name, tc := range map[string]struct {
		containerd bool
		least      int64
		// send reports to w, at(n) waiting until n windows after w began.
		send func(w *dockerPullWatch, at func(n float64))
		// stalled is whether the pull stalls, and after how many windows without progress at
		// the least, with at most a window late.
		stalled bool
		after   float64
	}{
		"a link at the floor in containerd's steps": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				w.report(dockerPullMessage{ID: "a", Status: "Pulling fs layer"})
				for i := int64(1); i <= 6; i++ {
					at(1.9 * float64(i))
					w.report(downloading("a", i<<20, total))
				}
			},
		},
		"a link at the floor in the classic store's steps": {
			least: 128 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				w.report(dockerPullMessage{ID: "a", Status: "Pulling fs layer"})
				for i := int64(1); i <= 4; i++ {
					at(3.8 * float64(i))
					w.report(downloading("a", i*512<<10, total))
				}
			},
		},
		"a registry that stops mid-layer": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, _ func(float64)) {
				w.report(downloading("a", 1<<20, total))
			},
			// A mebibyte behind Docker's step is three windows behind; the fourth ends it.
			stalled: true, after: 4,
		},
		"a registry that drips": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				for i := int64(1); i <= 40; i++ {
					w.report(downloading("a", i<<14, total))
					at(0.25 * float64(i))
				}
			},
			stalled: true, after: 3,
		},
		"a fast window followed by none": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, _ func(float64)) {
				w.report(downloading("a", 32<<20, total))
			},
			stalled: true, after: 4,
		},
		"Docker never naming a layer": {
			containerd: true, least: 512 << 10,
			send:    func(*dockerPullWatch, func(float64)) {},
			stalled: true, after: 3,
		},
		// Docker reports an attestation's blobs as it downloads them, while it may still wait on
		// the image's config, before it names any layer.
		"an attestation Docker downloads before it names a layer": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, _ func(float64)) {
				w.report(dockerPullMessage{ID: "c", Status: "Download complete"})
			},
			// A step for the attestation, then a mebibyte behind, three windows, ends it in the
			// fourth.
			stalled: true, after: 4,
		},
		// Docker's classic store fetches the config alongside the layers and waits for it after.
		"a config Docker waits for once it has unpacked the layers": {
			least: 128 << 10,
			send: func(w *dockerPullWatch, _ func(float64)) {
				w.report(dockerPullMessage{ID: "a", Status: "Pulling fs layer"})
				w.report(downloading("a", 512<<10, 512<<10))
				w.report(dockerPullMessage{ID: "a", Status: "Download complete"})
				w.report(dockerPullMessage{ID: "a", Status: "Extracting"})
				w.report(dockerPullMessage{ID: "a", Status: "Pull complete"})
			},
			// A step behind, four windows, ends it in the sixth, after the first's half mebibyte.
			stalled: true, after: 6,
		},
		"a config Docker waits for with every layer already there": {
			least: 128 << 10,
			send: func(w *dockerPullWatch, _ func(float64)) {
				w.report(dockerPullMessage{ID: "b", Status: "Already exists"})
			},
			stalled: true, after: 5,
		},
		"Docker unpacking what it has": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				w.report(dockerPullMessage{ID: "a", Status: "Pulling fs layer"})
				w.report(dockerPullMessage{ID: "c", Status: "Pulling fs layer"})
				w.report(downloading("a", 4<<20, 4<<20))
				w.report(dockerPullMessage{ID: "a", Status: "Download complete"})
				w.report(dockerPullMessage{ID: "b", Status: "Already exists"})
				w.report(dockerPullMessage{ID: "c", Status: "Already exists"})
				w.report(dockerPullMessage{ID: "a", Status: "Extracting"})
				at(8)
			},
		},
		"Docker unpacking a layer the image lists again": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				w.report(dockerPullMessage{ID: "a", Status: "Pulling fs layer"})
				w.report(downloading("a", 4<<20, 4<<20))
				w.report(dockerPullMessage{ID: "a", Status: "Download complete"})
				w.report(dockerPullMessage{ID: "a", Status: "Pull complete"})
				w.report(dockerPullMessage{ID: "a", Status: "Extracting"})
				at(8)
			},
		},
		// containerd writes a layer smaller than its mebibyte in one go, so Docker reports it
		// only once it has it all.
		"layers too small for Docker to report": {
			containerd: true, least: 256 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				for i := range 8 {
					id := fmt.Sprint("l", i)
					at(2 * float64(i))
					w.report(dockerPullMessage{ID: id, Status: "Pulling fs layer"})
					at(2*float64(i) + 1.5)
					w.report(dockerPullMessage{ID: id, Status: "Download complete"})
					w.report(dockerPullMessage{ID: id, Status: "Pull complete"})
				}
			},
		},
		"the rest of each layer, which Docker does not report": {
			containerd: true, least: 512 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				for i := range 4 {
					id := fmt.Sprint("l", i)
					at(3 * float64(i))
					w.report(dockerPullMessage{ID: id, Status: "Pulling fs layer"})
					at(3*float64(i) + 0.5)
					w.report(downloading(id, 1<<20, 1900<<10))
					at(3*float64(i) + 2.5)
					w.report(dockerPullMessage{ID: id, Status: "Download complete"})
					w.report(dockerPullMessage{ID: id, Status: "Pull complete"})
				}
			},
		},
		// Docker's classic store reports a resumed download whose blob does not match its digest
		// as checked, then downloads it again.
		"a registry that stops once Docker downloads a layer again": {
			least: 128 << 10,
			send: func(w *dockerPullWatch, _ func(float64)) {
				w.report(dockerPullMessage{ID: "a", Status: "Pulling fs layer"})
				w.report(downloading("a", 512<<10, 1<<20))
				w.report(dockerPullMessage{ID: "a", Status: "Verifying Checksum"})
				w.report(dockerPullMessage{ID: "a", Status: "Retrying in 5 seconds"})
			},
			// A step behind, four windows, ends it in the sixth, after the first's mebibyte.
			stalled: true, after: 6,
		},
		"Docker downloading a layer again": {
			least: 128 << 10,
			send: func(w *dockerPullWatch, at func(float64)) {
				for i := range 3 {
					w.report(downloading("a", 512<<10, 1<<30))
					w.report(dockerPullMessage{ID: "a", Status: "Retrying in 5 seconds"})
					at(2 * float64(i+1))
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var stalls atomic.Int32
			var stalledAt atomic.Int64
			began := time.Now()
			w := newDockerPullWatch(registryProgress{window: window, least: tc.least}, tc.containerd, func() {
				stalls.Add(1)
				stalledAt.Store(int64(time.Since(began)))
			})
			defer w.stop()
			tc.send(w, func(n float64) {
				time.Sleep(time.Until(began.Add(time.Duration(n * float64(window)))))
			})
			sent := time.Since(began)
			if !tc.stalled {
				if stalls.Load() != 0 {
					t.Fatal("stalled after", time.Duration(stalledAt.Load()))
				}
				return
			}
			deadline := time.Now().Add(time.Duration(tc.after+10) * window)
			for stalls.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(window / 10)
			}
			if stalls.Load() != 1 {
				t.Fatal("stalled", stalls.Load(), "times")
			}
			at := time.Duration(stalledAt.Load())
			if early := time.Duration(tc.after * float64(window)); at < early && sent < early {
				t.Fatal("stalled early, after", at)
			}
			if late := sent + time.Duration((tc.after+1)*float64(window)); at > late {
				t.Fatal("stalled late, after", at)
			}
			// What Docker reports after the end changes nothing.
			w.report(downloading("a", 1<<40, total))
			time.Sleep(2 * window)
			if stalls.Load() != 1 {
				t.Fatal("stalled again")
			}
		})
	}
}

// pullDaemon is Docker without any image, which answers each pull with pull. ended counts the
// pulls whose request Docker saw end.
type pullDaemon struct {
	docker *client.Client
	ended  atomic.Int32
}

func newPullDaemon(t *testing.T, pull func(w http.ResponseWriter, r *http.Request)) *pullDaemon {
	t.Helper()
	d := &pullDaemon{}
	// A socket's path may be far shorter than a test's own directory.
	sockets, err := os.MkdirTemp("", "docker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })
	listener, err := net.Listen("unix", filepath.Join(sockets, "s"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.54")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
		case strings.HasSuffix(r.URL.Path, "/info"):
			raw, _ := stablejson.Marshal(system.Info{OSType: "linux", Architecture: "x86_64", RegistryConfig: &registrytypes.ServiceConfig{}, DriverStatus: [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}})
			_, _ = w.Write(raw)
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			defer d.ended.Add(1)
			pull(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such image"}`))
		}
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	d.docker, err = client.New(client.WithHost("unix://" + listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.docker.Close() })
	return d
}

// letGo waits for the client to let go of Docker's pull, which Docker sees as the request ending
// soon after the client's call returns.
func (d *pullDaemon) letGo(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); d.ended.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("Docker's pull did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stalls is a pull that receives a mebibyte of its one layer, then nothing more until its request
// ends.
func stalls(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `{"status":"Pulling from library/busybox","id":"latest"}`+"\n")
	fmt.Fprint(w, `{"status":"Pulling fs layer","progressDetail":{},"id":"a"}`+"\n")
	fmt.Fprint(w, `{"status":"Downloading","progressDetail":{"current":1048576,"total":67108864},"id":"a"}`+"\n")
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

// keepsUp is a pull that receives a mebibyte of its layer every 20 milliseconds, more than any
// floor here asks, until its request ends.
func keepsUp(w http.ResponseWriter, r *http.Request) {
	for i := 1; ; i++ {
		fmt.Fprintf(w, `{"status":"Downloading","progressDetail":{"current":%d,"total":1099511627776},"id":"a"}`+"\n", i<<20)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// pullCommand is a deployment's command whose journal record its owner has not cancelled.
func pullCommand(t *testing.T, e *Engine) (api.ExecutionCommand, deployment.Plan) {
	t.Helper()
	journal, plan := testVisualJournal(t)
	e.Journal = journal
	c := api.ExecutionCommand{Id: uuid.New(), Image: execution.TestImage}
	if err := journal.db.Update(func(tx *bolt.Tx) error {
		return journal.put(tx, "operations", c.Id.String(), Record{Command: c, Checkpoint: "accepted"})
	}); err != nil {
		t.Fatal(err)
	}
	return c, plan
}

// cancelAfter has the owner cancel c after wait, and returns when it did.
func cancelAfter(t *testing.T, e *Engine, c api.ExecutionCommand, wait time.Duration) <-chan time.Time {
	t.Helper()
	at := make(chan time.Time, 1)
	time.AfterFunc(wait, func() {
		if err := e.Journal.Mutate(c.Id, func(r *Record) { r.Command.CancelRequested = true }); err != nil {
			t.Error(err)
		}
		at <- time.Now()
	})
	return at
}

// A deployment whose image Docker pulls itself fails, changing nothing, once its registry stops
// sending, once its owner cancels it, and once it runs out of time, and Docker's pull ends with it.
func TestDeploymentEndsDockersPull(t *testing.T) {
	for name, tc := range map[string]struct {
		pull   func(http.ResponseWriter, *http.Request)
		engine *Engine
		cancel bool
		state  api.ExecutionState
		note   string
	}{
		"a registry that stops": {
			pull:   stalls,
			engine: &Engine{registryProgress: &registryProgress{window: 50 * time.Millisecond, least: 1 << 20}},
			state:  "failed", note: "its registry stopped sending it to Docker",
		},
		"its owner cancelling it": {
			pull:   stalls,
			cancel: true,
			state:  "cancelled", note: errPullCancelled.Error(),
		},
		"its time running out": {
			pull:   keepsUp,
			engine: &Engine{pullLimit: 300 * time.Millisecond},
			state:  "failed", note: "took longer than the hour the agent allows",
		},
	} {
		t.Run(name, func(t *testing.T) {
			daemon := newPullDaemon(t, tc.pull)
			e := tc.engine
			if e == nil {
				e = &Engine{}
			}
			e.Docker = daemon.docker
			c, plan := pullCommand(t, e)
			var cancelled <-chan time.Time
			if tc.cancel {
				cancelled = cancelAfter(t, e, c, 300*time.Millisecond)
			}
			began := time.Now()
			err := e.pullVisualImages(t.Context(), c, deployment.Payload{Plan: plan})
			if cancelled != nil {
				if waited := time.Since(<-cancelled); waited > time.Second {
					t.Fatal("cancelled after", waited)
				}
			} else if waited := time.Since(began); waited > 5*time.Second {
				t.Fatal("ended after", waited)
			}
			state, note := pullFailure(err, nil, true)
			if state != tc.state || !strings.Contains(note, tc.note) || !strings.Contains(note, "Existing workloads are unchanged") {
				t.Fatal(state, note)
			}
			daemon.letGo(t)
		})
	}
}

// The test command's pull ends as its owner cancels it, a stall or its time ends it, or Docker
// fails it, with no container created, and only a cancel ends it other than failed; an agent that
// stops leaves it to pull again when it starts.
func TestTestCommandEndsDockersPull(t *testing.T) {
	for name, tc := range map[string]struct {
		pull   func(http.ResponseWriter, *http.Request)
		engine *Engine
		cancel bool
		stop   bool
		state  api.ExecutionState
		note   string
	}{
		"its owner cancelling it": {pull: stalls, cancel: true, state: "cancelled", note: "Cancelled while Docker pulled the test image. No container was created."},
		"a registry that stops": {
			pull:   stalls,
			engine: &Engine{registryProgress: &registryProgress{window: 50 * time.Millisecond, least: 1 << 20}},
			state:  "failed", note: "the registry stopped sending the test image",
		},
		"its time running out": {pull: keepsUp, engine: &Engine{pullLimit: 300 * time.Millisecond}, state: "failed", note: "within the 10 minutes the agent allows"},
		"Docker failing it": {
			pull: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"errorDetail":{"message":"no space left on device"},"error":"no space left on device"}`+"\n")
			},
			state: "failed", note: "Image pull failed. No container was created.",
		},
		"Docker refusing it": {
			pull: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"message":"pull access denied"}`, http.StatusNotFound)
			},
			state: "failed", note: "Image pull failed. No container was created.",
		},
		"the agent stopping": {pull: stalls, stop: true},
	} {
		t.Run(name, func(t *testing.T) {
			daemon := newPullDaemon(t, tc.pull)
			e := tc.engine
			if e == nil {
				e = &Engine{}
			}
			e.Docker = daemon.docker
			c, _ := pullCommand(t, e)
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			var cancelled <-chan time.Time
			if tc.cancel {
				cancelled = cancelAfter(t, e, c, 300*time.Millisecond)
			}
			if tc.stop {
				time.AfterFunc(300*time.Millisecond, stop)
			}
			r, _ := e.Journal.Record(c.Id)
			err := e.start(ctx, c, r)
			if cancelled != nil {
				if waited := time.Since(<-cancelled); waited > time.Second {
					t.Fatal("cancelled after", waited)
				}
			}
			r, _ = e.Journal.Record(c.Id)
			if tc.stop {
				if !errors.Is(err, context.Canceled) || r.Report.State != "" || r.Checkpoint != "pull" {
					t.Fatal(err, r.Report.State, r.Checkpoint)
				}
				return
			}
			if err != nil || r.Report.State != tc.state || !strings.Contains(r.Report.Note, tc.note) {
				t.Fatal(err, r.Report.State, r.Report.Note)
			}
		})
	}
}

// Pulls a command makes part-way through its work, the proxy's while it publishes routes and a
// database release's in a restore or an upgrade, keep the engine to the command, and still end
// once their registry stops sending or their time runs out.
func TestUnlentPullsEndAsDockersPullsDo(t *testing.T) {
	paths := map[string]func(*testing.T, *Engine) error{
		"the proxy's":            func(t *testing.T, e *Engine) error { return e.pullProxyImage(t.Context()) },
		"a restore or upgrade's": func(t *testing.T, e *Engine) error { return e.ensureImage(t.Context(), "postgres:18.1") },
		"the test command's": func(t *testing.T, e *Engine) error {
			c, _ := pullCommand(t, e)
			r, _ := e.Journal.Record(c.Id)
			if err := e.start(t.Context(), c, r); err != nil {
				return err
			}
			r, _ = e.Journal.Record(c.Id)
			return errors.New(r.Report.Note)
		},
	}
	for path, pull := range paths {
		for name, tc := range map[string]struct {
			pull   func(http.ResponseWriter, *http.Request)
			engine *Engine
			want   error
			note   string
		}{
			"a registry that stops": {
				pull:   stalls,
				engine: &Engine{registryProgress: &registryProgress{window: 50 * time.Millisecond, least: 1 << 20}},
				want:   errDockerPullStalled, note: "stopped sending",
			},
			"its time running out": {pull: keepsUp, engine: &Engine{pullLimit: 300 * time.Millisecond}, want: errPullTimedOut, note: "within the 10 minutes"},
		} {
			t.Run(path+" with "+name, func(t *testing.T) {
				e := tc.engine
				var lent atomic.Bool
				daemon := newPullDaemon(t, func(w http.ResponseWriter, r *http.Request) {
					e.lendMu.Lock()
					lent.Store(e.lent)
					e.lendMu.Unlock()
					tc.pull(w, r)
				})
				e.Docker = daemon.docker
				began := time.Now()
				err := pull(t, e)
				if path == "the test command's" {
					if err == nil || !strings.Contains(err.Error(), tc.note) {
						t.Fatal(err)
					}
				} else if !errors.Is(err, tc.want) {
					t.Fatal(err)
				}
				if waited := time.Since(began); waited > 5*time.Second {
					t.Fatal("ended after", waited)
				}
				if daemon.letGo(t); daemon.ended.Load() != 1 || lent.Load() {
					t.Fatal("pulls", daemon.ended.Load(), "lent", lent.Load())
				}
			})
		}
	}
}
