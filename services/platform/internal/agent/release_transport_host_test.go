package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// Forward real Docker effects, then lose one reply. An API mock cannot prove
// that a command actually started and stopped within its reviewed budget.
func TestNativeReleaseCommandTransport(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires owned disposable native host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root on disposable Ubuntu")
	}
	host := os.Getenv("PLAT_APP_DOCKER_HOST")
	if !strings.HasPrefix(host, "unix:///") {
		t.Fatal("select the disposable Docker socket")
	}
	actual, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	defer actual.Close()
	for _, lost := range []string{"start", "inspect", "offline"} {
		t.Run(lost, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			j, p := testVisualJournal(t)
			app := uuid.New()
			image := execution.TestImage
			p.Specification.Applications = []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
			p.Images = map[string]string{app.String(): image}
			p.Instances = map[string]uuid.UUID{app.String(): uuid.New()}
			options := deployment.DefaultReleaseOptions()
			options.Command = []string{"sh", "-c", "sleep 600"}
			options.CommandTimeoutSeconds = 1
			p.Release = &deployment.Release{ApplicationID: app, PreviousPlanID: uuid.New(), Options: options}
			p = p.WithHash()
			c := visualCommand(p, "deploy", 1)
			payload := deployment.Payload{Action: "deploy", Plan: p}
			state := releaseState{OperationID: c.Id, Plan: p, Phase: "ready"}
			t.Cleanup(func() {
				clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				v, err := actual.ContainerList(clean, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", labelServer+"="+p.ServerID.String())})
				if err != nil {
					t.Error(err)
					return
				}
				for _, item := range v.Items {
					if item.Labels[labelServer] == p.ServerID.String() {
						_, _ = actual.ContainerRemove(clean, item.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
					}
				}
				n, err := actual.NetworkInspect(clean, deployment.NetworkName(p.UnitID), client.NetworkInspectOptions{})
				if err == nil && visualOwned(n.Network.Labels, p) {
					_, _ = actual.NetworkRemove(clean, n.Network.ID, client.NetworkRemoveOptions{})
				}
			})
			direct := &Engine{Journal: j, Docker: actual}
			if err = direct.ensureStorage(ctx, p); err != nil {
				t.Fatal(err)
			}
			upstream := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(host, "unix://"))
			}}
			defer upstream.CloseIdleConnections()
			var started, dropped atomic.Bool
			var starts atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				isStart := strings.HasSuffix(r.URL.Path, "/containers/"+releaseCommandName(c)+"/start")
				isInspect := strings.HasSuffix(r.URL.Path, "/containers/"+releaseCommandName(c)+"/json")
				if (lost == "inspect" || lost == "offline") && isInspect && started.Load() && dropped.CompareAndSwap(false, true) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(500)
					io.WriteString(w, `{"message":"transient fixture inspection failure"}`)
					return
				}
				req := r.Clone(r.Context())
				req.URL.Scheme, req.URL.Host = "http", "docker"
				req.RequestURI = ""
				response, err := upstream.RoundTrip(req)
				if err != nil {
					http.Error(w, "fixture upstream unavailable", 502)
					return
				}
				defer response.Body.Close()
				if isStart {
					starts.Add(1)
					started.Store(true)
					if lost == "start" && dropped.CompareAndSwap(false, true) {
						io.Copy(io.Discard, response.Body)
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						connection.Close()
						return
					}
				}
				for key, values := range response.Header {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(response.StatusCode)
				io.Copy(w, response.Body)
			}))
			defer proxy.Close()
			docker, err := client.New(client.WithHost(proxy.URL), client.WithAPIVersion("1.54"))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			engine := &Engine{Journal: j, Docker: docker}
			began := time.Now()
			outcome := ""
			if lost == "offline" {
				if _, err = j.Accept(c); err != nil {
					t.Fatal(err)
				}
				if err = j.saveRelease(state); err != nil {
					t.Fatal(err)
				}
				var managementCalls atomic.Int32
				management := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					managementCalls.Add(1)
					http.Error(w, "controller unavailable", 503)
				}))
				defer management.Close()
				cfg, err := j.Config()
				if err != nil {
					t.Fatal(err)
				}
				cfg.Enrolled = true
				cfg.Origin = management.URL
				cfg.Credential = strings.Repeat("a", 43)
				if err = j.SetConfig(cfg); err != nil {
					t.Fatal(err)
				}
				agent := NewClient(j, engine)
				agent.HTTP = management.Client()
				agent.Observe = func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
				runctx, stop := context.WithCancel(ctx)
				done := make(chan error, 1)
				go func() { done <- agent.Run(runctx) }()
				for time.Since(began) < 15*time.Second && ctx.Err() == nil {
					record, e := j.Record(c.Id)
					if e != nil {
						t.Fatal(e)
					}
					if record.Report.State == "failed" {
						outcome = "failed"
						break
					}
					if record.Report.State == "needs_attention" {
						outcome = "uncertain"
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				stop()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("agent did not stop")
				}
				if managementCalls.Load() == 0 {
					t.Fatal("offline management boundary not exercised")
				}
			} else {
				for range 3 {
					outcome, err = engine.releaseCommand(ctx, c, payload, &state)
					if err == nil {
						break
					}
				}
			}
			if err != nil || outcome != "failed" || !dropped.Load() || starts.Load() != 1 {
				t.Fatalf("lost reply abandoned or replayed command: outcome=%s err=%v starts=%d dropped=%v", outcome, err, starts.Load(), dropped.Load())
			}
			inspected, err := actual.ContainerInspect(ctx, releaseCommandName(c), client.ContainerInspectOptions{})
			if (err != nil && !(lost == "offline" && errdefs.IsNotFound(err))) || err == nil && inspected.Container.State.Running || time.Since(began) > 15*time.Second {
				t.Fatalf("deadline did not stop real command: %v", err)
			}
		})
	}
}
