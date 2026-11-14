package agent

import (
	"context"
	"encoding/json/jsontext"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// PLAT-357, PLAT-360, PLAT-366 and PLAT-369. A coordinated backup leaf whose command stops while
// the agent runs on settles by what its run did and whether its cleanup was confirmed, and the
// primary's reservation and writers follow:
//   - stopped by the owner before its first effect, the leaf is cancelled with its run failed,
//     and resuming the source releases the host;
//   - stopped by the owner part way through its upload, with nothing of the run left running, the
//     leaf is cancelled with its run failed, and resuming the source releases the host;
//   - stopped the same way while a container labelled with the run can't be removed, the leaf and
//     its run need attention, the host stays held with its writer stopped, and once the owner
//     removes the container and rejects the leaf, resumption releases it;
//   - taken up only in its last seconds, the leaf runs past its deadline, because its work is
//     bounded from when the agent admitted it, and succeeds;
//   - a run that reaches its own timeout fails, and resuming the source releases the host with
//     no one inspecting it.
//
// The parent's resumption of other hosts after a rejection is covered by
// TestNativeCrossHostDeployment's interrupted coordinated backup.
func TestNativeCoordinatedBackupStops(t *testing.T) {
	for _, stop := range []struct {
		name, at, by string
		leaf, run    api.ExecutionState
	}{
		{name: "owner_cancel_before_first_effect", at: "backup-run-recorded", by: "owner", leaf: "cancelled", run: "failed"},
		{name: "owner_cancel_mid_upload", at: "backup-uploading", by: "owner", leaf: "cancelled", run: "failed"},
		{name: "owner_cancel_mid_upload_cleanup_unconfirmed", at: "backup-uploading", by: "owner-unremovable", leaf: "needs_attention", run: "needs_attention"},
		{name: "late_start_runs_past_its_deadline", at: "backup-uploading", by: "deadline", leaf: "succeeded", run: "succeeded"},
		{name: "run_timeout_mid_upload", at: "backup-uploading", by: "timeout", leaf: "failed", run: "failed"},
	} {
		t.Run(stop.name, func(t *testing.T) {
			volume := uuid.New()
			w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/data"}}}, nil)
			e := w.engine
			e.ResticCLI = "/opt/astilba-platform-tools/restic"
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			e.BackupRoots = []string{root}
			policy := backup.Policy{ID: uuid.New(), Name: "Coordinated files", WorkspaceID: uuid.New(), EnvironmentID: w.plan.EnvironmentID, ServerID: w.plan.ServerID, DestinationID: uuid.New(), ResourceID: volume, Generation: 1, Method: backup.QuiescedFiles, Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Coordinated: true, Plan: w.plan}
			if stop.by == "timeout" {
				policy.TimeoutSeconds = 10
			}
			if err := policy.Validate(); err != nil {
				t.Fatal(err)
			}
			installation := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "Fixture", Kind: "filesystem", Root: root}, Access: backup.Access{Password: "disposable-repository-canary"}}
			install := w.backupCommand(backup.Command{Action: "install", Installation: installation}, false)
			if r := w.deliver(install); r.State != "succeeded" {
				t.Fatalf("install ended %s: %s", r.State, r.Note)
			} else if err := w.journal.Acknowledge(install.Id, r.Sequence); err != nil {
				t.Fatal(err)
			}
			effect := backup.Command{Action: "backup", Installation: installation, RunID: uuid.New()}
			w.intent = deployment.Maintenance{Kind: "backup", ResourceID: policy.ID, RunID: effect.RunID, EffectHash: effect.Digest()}
			w.observeForManagement()
			w.phase("reserve")
			w.phase("pause")
			if w.running() {
				t.Fatal("the pause left the writer running")
			}
			c := w.freeze(deployment.CrossHostCommand{ParentID: w.parent, GroupHash: w.root, Phase: "effect", EffectAction: "backup", Unit: deployment.DistributedUnit{Plan: w.plan}, Maintenance: &w.intent})
			if r := w.deliver(c); r.State != "succeeded" {
				t.Fatalf("effect ended %s: %s", r.State, r.Note)
			} else if err := w.journal.Acknowledge(c.Id, r.Sequence); err != nil {
				t.Fatal(err)
			}

			// As management issues it: the leaf outlives its run's own timeout.
			leaf := w.backupCommand(effect, true)
			var stall time.Time
			leftOver := ""
			switch stop.by {
			case "deadline":
				// The agent took up the leaf only in its last seconds.
				leaf.Deadline = time.Now().Add(10 * time.Second)
				stall = leaf.Deadline.Add(2 * time.Second)
			case "timeout":
				stall = time.Now().Add(policy.RunTimeout() + 3*time.Second)
			}
			e.Point = func(point string) {
				if point != stop.at {
					return
				}
				if stop.by == "owner-unremovable" {
					// A container of the run that Docker refuses to remove.
					leftOver = "astilba-leftover-" + effect.RunID.String()
					w.runContainer(leftOver, policy, effect.RunID)
					e.Docker = removalRefusingDocker(t)
				}
				if strings.HasPrefix(stop.by, "owner") {
					cancelled := leaf
					cancelled.CancelRequested = true
					if _, err := w.journal.Accept(cancelled); err != nil {
						t.Error(err)
					}
					// Long enough for the command to see its cancel, which it polls for.
					stall = time.Now().Add(time.Second)
				}
				time.Sleep(time.Until(stall))
			}
			report := w.deliver(leaf)
			e.Point, e.Docker = nil, w.docker
			if report.State != stop.leaf {
				t.Fatalf("the stopped leaf settled as %s: %s", report.State, report.Note)
			}
			stored, err := w.journal.schedule(policy.ID)
			if err != nil || len(stored.Runs) != 1 || stored.Runs[0].ID != effect.RunID || stored.Runs[0].State != string(stop.run) {
				t.Fatalf("the stopped leaf's run settled as %+v (%v)", stored.Runs, err)
			}
			if stop.leaf == "needs_attention" {
				if !strings.Contains(stored.Runs[0].Note, leftOver) {
					t.Fatal("the run's note doesn't name the container it couldn't remove:", stored.Runs[0].Note)
				}
				// The parent waits on the uncertain leaf, so nothing resumes the writer or
				// releases the host until the owner removes what the run left and rejects it.
				if w.running() || !w.barrier() {
					t.Fatal("an uncertain leaf released its host")
				}
				if _, err = w.docker.ContainerRemove(context.Background(), leftOver, client.ContainerRemoveOptions{Force: true}); err != nil {
					t.Fatal(err)
				}
				if err = e.RejectCrossHostEffect(context.Background(), leaf.Id); err != nil {
					t.Fatal(err)
				}
				if report = w.deliver(leaf); report.State != "failed" {
					t.Fatalf("the rejected leaf settled as %s", report.State)
				}
				if stored, err = w.journal.schedule(policy.ID); err != nil || stored.Runs[0].State != "failed" {
					t.Fatalf("the rejected run settled as %+v (%v)", stored.Runs, err)
				}
			}
			if err = w.journal.Acknowledge(leaf.Id, report.Sequence); err != nil {
				t.Fatal(err)
			}
			// A settled leaf moves the parent to resume the source.
			w.phase("resume-source")
			w.phase("complete")
			if w.barrier() || !w.running() {
				t.Fatal("the settled backup left its host reserved or its writer stopped")
			}
		})
	}
}

// backupCommand makes the backup command management would deliver for b, at the next
// generation: under the reservation's parent when it is the coordinated leaf.
func (w *unreadyWriter) backupCommand(b backup.Command, leaf bool) api.ExecutionCommand {
	raw, _ := stablejson.Marshal(b)
	payload := jsontext.Value(raw)
	w.generation++
	controller := int64(1)
	p := b.Installation.Policy
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.ID, Kind: "backup", Action: "inspect", Generation: w.generation, ControllerGeneration: &controller, PlanHash: b.Digest(), Deadline: time.Now().Add(b.Lifetime()), Backup: &payload}
	if p.GeneralSchedule() {
		c.Kind = "schedule"
	}
	if leaf {
		c.ParentId, c.RecoveryCheckpoint = &w.parent, &w.descriptor
	}
	return c
}

// runContainer starts a container labelled as run's data helper is.
func (w *unreadyWriter) runContainer(name string, p backup.Policy, run uuid.UUID) {
	w.t.Helper()
	ctx := context.Background()
	labels := visualLabels(p.Plan, p.ResourceID)
	labels[labelKind], labels["com.astilba.run"] = "backup-data-v1", run.String()
	created, err := w.docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "3600"}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() {
		_, _ = w.docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = w.docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		w.t.Fatal(err)
	}
}

// removalRefusingDocker is the test host's Docker, except that it refuses to remove containers.
func removalRefusingDocker(t *testing.T) *client.Client {
	return refusingDocker(t, func(r *http.Request) bool {
		return r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/")
	})
}

// refusingDocker is the test host's Docker, except that it refuses the requests refuse picks.
func refusingDocker(t *testing.T, refuse func(*http.Request) bool) *client.Client {
	t.Helper()
	host, err := client.ParseHostURL(os.Getenv("PLAT_APP_DOCKER_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) { r.Out.URL.Scheme, r.Out.URL.Host = "http", "docker" },
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, host.Scheme, host.Host)
		}},
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse(r) {
			http.Error(w, `{"message":"refused"}`, http.StatusInternalServerError)
			return
		}
		daemon.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	docker, err := client.New(client.WithHost("tcp://" + proxy.Listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return docker
}
