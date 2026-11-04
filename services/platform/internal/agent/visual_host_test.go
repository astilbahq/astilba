package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// nativeVisualFixture owns a journal, Docker resources and a monotonically
// increasing command stream on a disposable native Linux host.
type nativeVisualFixture struct {
	t             *testing.T
	docker        *client.Client
	state         string
	dockerCLI     string
	server        uuid.UUID
	engine        *Engine
	generation    int64
	requestClient *http.Client
}

// Run only on the disposable native Linux host: this fixture publishes ports.
func newNativeVisualFixture(t *testing.T) *nativeVisualFixture {
	t.Helper()
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root in the disposable Ubuntu host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	if _, err = docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{}); err == nil {
		t.Fatal("fixture requires absent ingress container")
	}
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeVisualFixture{t: t, docker: docker, state: state, dockerCLI: "docker", server: uuid.New(), engine: &Engine{Journal: j, Docker: docker}, requestClient: &http.Client{Timeout: 2 * time.Second}}
	t.Cleanup(func() { f.engine.Journal.Close() })
	if err = j.SetConfig(Config{ServerID: f.server, MachineID: uuid.New(), Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	// CI installs its pinned toolchain separately from the runner's Docker.
	if _, err := os.Stat("/opt/astilba-plat4/bin/docker"); err == nil {
		config := filepath.Join(state, "fixture-docker")
		if err = os.Mkdir(config, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"cliPluginsExtraDirs":["/opt/astilba-plat4/cli-plugins"]}`), 0600); err != nil {
			t.Fatal(err)
		}
		f.dockerCLI = filepath.Join(state, "docker")
		script := fmt.Sprintf("#!/bin/sh\nexport DOCKER_CONFIG='%s'\nexec /opt/astilba-plat4/bin/docker \"$@\"\n", config)
		if err = os.WriteFile(f.dockerCLI, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.engine.DockerCLI = f.dockerCLI
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		filter := make(client.Filters).Add("label", labelServer+"="+f.server.String())
		if all, e := docker.ContainerList(cleanup, client.ContainerListOptions{All: true, Filters: filter}); e == nil {
			for _, v := range all.Items {
				_, _ = docker.ContainerRemove(cleanup, v.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
			}
		}
		if all, e := docker.NetworkList(cleanup, client.NetworkListOptions{Filters: filter}); e == nil {
			for _, v := range all.Items {
				_, _ = docker.NetworkRemove(cleanup, v.ID, client.NetworkRemoveOptions{})
			}
		}
		if all, e := docker.VolumeList(cleanup, client.VolumeListOptions{Filters: filter}); e == nil {
			for _, v := range all.Items {
				_, _ = docker.VolumeRemove(cleanup, v.Name, client.VolumeRemoveOptions{})
			}
		}
	})
	return f
}

func (f *nativeVisualFixture) command(action string, p deployment.Plan, secrets map[string]string) api.ExecutionCommand {
	f.generation++
	payload := deployment.Payload{Action: action, Plan: p, Secrets: secrets}
	raw, _ := json.Marshal(payload)
	v := json.RawMessage(raw)
	return api.ExecutionCommand{Id: uuid.New(), ServerId: f.server, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "visual", Action: "inspect", Generation: f.generation, PlanHash: payload.Digest(), Visual: &v, Deadline: time.Now().Add(time.Minute)}
}

func (f *nativeVisualFixture) finish(c api.ExecutionCommand) Record {
	f.t.Helper()
	if err := f.engine.Execute(context.Background(), c); err != nil {
		f.t.Fatal(err)
	}
	r, err := f.engine.Journal.Record(c.Id)
	if err != nil {
		f.t.Fatal(err)
	}
	if r.Report.State != "succeeded" {
		f.t.Fatalf("%s failed: %s", c.Kind, r.Report.Note)
	}
	if err = f.engine.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *nativeVisualFixture) plan(name string, withDB bool) (deployment.Plan, map[string]string) {
	app, pg, vol, secret := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	image := execution.TestImage
	pgimage := domain.PostgresImage
	value := "v1"
	script := `mkdir -p /tmp/www/cgi-bin
printf '%s' "$VALUE" > /tmp/www/index.html
printf ready > /tmp/www/health
cat > /tmp/www/cgi-bin/identity <<'CGI'
#!/bin/sh
printf 'Content-Type: text/plain\r\n\r\n'
hostname
CGI
chmod +x /tmp/www/cgi-bin/identity
echo "$DATABASE_URL"
exec httpd -f -p 8080 -h /tmp/www`
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "VALUE", Value: &value}}, Command: []string{"sh", "-c", script}, Health: &domain.HealthCheck{Port: 8080, Path: "/health"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: name + ".example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	secrets := map[string]string{}
	if withDB {
		spec.Volumes = []domain.Volume{{ID: vol, Name: "Data"}}
		spec.Applications = append(spec.Applications, domain.Application{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgimage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: "/var/lib/postgresql"}}})
		spec.Bindings = []domain.Binding{{ID: uuid.New(), SourceID: pg, TargetID: app, Variable: "DATABASE_URL"}}
		secrets[secret.String()] = "native/$secret@canary"
		spec.Routes = append(spec.Routes, domain.Route{ID: uuid.New(), TargetID: pg, Protocol: "tcp", Domain: "db.example.test", Port: 5432, ListenPort: 15432, TLS: "off", AllowCIDRs: []string{"0.0.0.0/0"}})
	}
	p := deployment.Plan{ID: uuid.New(), ServerID: f.server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{}, HostChanges: true, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
	return p, secrets
}

func (f *nativeVisualFixture) resolve(p deployment.Plan) deployment.Plan {
	f.t.Helper()
	r := f.finish(f.command("resolve", p, nil))
	var observed deployment.Observation
	if err := json.Unmarshal(*r.Report.Observation, &observed); err != nil {
		f.t.Fatal(err)
	}
	p.Images = observed.Images
	p.ExpectedState = observed.StateHash
	return p.WithHash()
}

func (f *nativeVisualFixture) request(host, want string) {
	f.t.Helper()
	req, _ := http.NewRequest("GET", "http://127.0.0.1/", nil)
	req.Host = host
	res, err := f.requestClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 512))
	if err != nil {
		f.t.Fatal(err)
	}
	if res.StatusCode != 200 || string(b) != want {
		f.t.Fatalf("route %s: %d %q", host, res.StatusCode, b)
	}
}

// The fixture must already have transferred to controller generation 1.
func (f *nativeVisualFixture) refuseInUseVolumeDeletion(p deployment.Plan) {
	f.t.Helper()
	deletion := f.command("delete-volume", p, nil)
	controllerGeneration := int64(1)
	deletion.ControllerGeneration = &controllerGeneration
	var payload deployment.Payload
	_ = json.Unmarshal(*deletion.Visual, &payload)
	volumeID := p.Specification.Volumes[0].ID
	payload.VolumeID = &volumeID
	raw, _ := json.Marshal(payload)
	message := json.RawMessage(raw)
	deletion.Visual = &message
	deletion.PlanHash = payload.Digest()
	if err := f.engine.Execute(context.Background(), deletion); err != nil {
		f.t.Fatal(err)
	}
	deleted, err := f.engine.Journal.Record(deletion.Id)
	if err != nil || deleted.Report.State != "needs_attention" {
		f.t.Fatal("in-use volume deletion was not refused")
	}
}

func TestNativeVisualDeployment(t *testing.T) {
	f := newNativeVisualFixture(t)
	ctx := context.Background()
	docker, state, server, dockerCLI := f.docker, f.state, f.server, f.dockerCLI
	command, finish, resolve, plan, request := f.command, f.finish, f.resolve, f.plan, f.request
	p, secrets := plan("first", true)
	p.Specification.Applications[0].Replicas = 3
	p.PrepareInstances(deployment.Plan{})
	p = resolve(p.WithHash())
	abandoned := append([]uuid.UUID(nil), p.InstanceIDs(p.Specification.Applications[0].ID)[1:]...)
	nativeAbandonReplicaStart(t, f.engine, p, command("deploy", p, secrets))
	p.ID, p.RevisionID = uuid.New(), uuid.New()
	p.PrepareInstances(deployment.Plan{})
	p = resolve(p.WithHash())
	r := finish(command("deploy", p, secrets))
	for _, id := range abandoned {
		if _, err := docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{}); err == nil {
			t.Fatal("abandoned initial replica survived successful reconciliation")
		}
	}
	request("first.example.test", "v1")
	t.Run("replica_distribution_and_health", func(t *testing.T) { nativeReplicaDistribution(t, f.engine, p) })
	if strings.Contains(string(*r.Report.Observation), "canary") || strings.Contains(string(*r.Report.Observation), "native%2F") {
		t.Fatal("observation leaked database credentials")
	}
	pg := p.Specification.Applications[1].ID
	password := secrets[p.Specification.Applications[1].Postgres.PasswordSecretVersionID.String()]
	webState, err := docker.ContainerInspect(ctx, deployment.UnitName(p.Specification.Applications[0].ID), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bindingOK := false
	for _, entry := range webState.Container.Config.Env {
		if value, ok := strings.CutPrefix(entry, "DATABASE_URL="); ok {
			parsed, err := url.Parse(value)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := parsed.User.Password()
			bindingOK = got == password && parsed.Host == deployment.ServiceName(pg)+":5432"
		}
	}
	if !bindingOK {
		t.Fatal("deployed database binding differs from its frozen values")
	}
	t.Run("resource_metrics", func(t *testing.T) {
		collector := &MetricCollector{Journal: f.engine.Journal}
		collection, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		sample := collector.Collect(collection, docker, server, state)
		if sample.Host.Memory == nil || sample.Host.Disk == nil || sample.Host.DiskSource != "docker" || len(sample.Services) != 2 || sample.Partial {
			t.Fatalf("native metric collection incomplete: %+v", sample)
		}
		for _, service := range sample.Services {
			if service.MemoryUsedBytes == nil || service.Health == nil || service.State != "running" {
				t.Fatal("native service metrics unavailable")
			}
		}
	})

	databaseURL := url.URL{Scheme: "postgres", Host: "127.0.0.1:15432", Path: "/app", User: url.UserPassword("app", password), RawQuery: "sslmode=disable"}
	t.Run("catalog", func(t *testing.T) { nativeCatalogJourney(t, f.engine, server, command) })
	db, err := pgx.Connect(ctx, databaseURL.String())
	if err != nil {
		t.Fatal("TCP route did not connect to PostgreSQL")
	}
	if _, err = db.Exec(ctx, "CREATE TABLE fixture(value text); INSERT INTO fixture VALUES ('data-survived')"); err != nil {
		t.Fatal(err)
	}
	db.Close(ctx)
	t.Run("database_metadata_restore", func(t *testing.T) {
		nativePostgresMetadataRestore(t, f.engine, p)
	})
	t.Run("backup_and_fresh_restore", func(t *testing.T) {
		nativeBackupJourney(t, f.engine, p, func(b backup.Command) Record {
			f.generation++
			raw, _ := json.Marshal(b)
			value := json.RawMessage(raw)
			return finish(api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: p.EnvironmentID, ResourceId: b.Installation.Policy.ID, Kind: "backup", Action: "inspect", Generation: f.generation, PlanHash: b.Digest(), Backup: &value, Deadline: time.Now().Add(time.Minute)})
		})
	})
	nativeFilesAndJobs(t, f.engine, server)
	other, otherSecrets := plan("second", false)
	other = resolve(other)
	finish(command("deploy", other, otherSecrets))
	request("first.example.test", "v1")
	request("second.example.test", "v1")
	// A manual network disconnect is visible without repairing it on inspection.
	web := p.Specification.Applications[0].ID
	if _, err = docker.NetworkDisconnect(ctx, deployment.NetworkName(p.UnitID), client.NetworkDisconnectOptions{Container: deployment.UnitName(web)}); err != nil {
		t.Fatal(err)
	}
	r = finish(command("observe", p, nil))
	var observed deployment.Observation
	_ = json.Unmarshal(*r.Report.Observation, &observed)
	drift := false
	for _, resource := range observed.Resources {
		drift = drift || resource.Drift
	}
	if !drift {
		t.Fatal("manual Docker change not reported")
	}
	p.ID = uuid.New()
	p.RevisionID = uuid.New()
	p.AcceptDrift = true
	value2 := "v2"
	p.Specification.Applications[0].Variables[0].Value = &value2
	p.Images = map[string]string{}
	p = p.WithHash()
	p = resolve(p)
	finish(command("deploy", p, secrets))
	// A rejected runtime reload restores the preceding routes before reporting failure.
	next := p
	next.ID = uuid.New()
	next.RevisionID = uuid.New()
	next.Specification.Routes = append([]domain.Route(nil), p.Specification.Routes...)
	next.Specification.Routes[0].Domain = "replacement.example.test"
	next.Specification.Routes[1].AllowCIDRs = []string{"192.0.2.0/24"}
	next.Images = map[string]string{}
	next = resolve(next.WithHash())
	c := command("deploy", next, secrets)
	injected := false
	f.engine.Point = func(point string) {
		if point == "route-publishing" && !injected {
			if _, err := docker.ContainerStop(ctx, proxyName, client.ContainerStopOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if point == "route-published" && !injected {
			injected = true
			if err := os.WriteFile(f.engine.routePath(), []byte("{invalid"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = f.engine.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	r, err = f.engine.Journal.Record(c.Id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Report.State != "needs_attention" || !strings.Contains(r.Report.Note, "restored") {
		t.Fatalf("reload was not rejected and recovered: %s", r.Report.Note)
	}
	request("first.example.test", "v2")
	request("second.example.test", "v1")
	f.engine.Point = nil
	if err = f.engine.Journal.Mutate(c.Id, func(r *Record) { r.Report.State = "running" }); err != nil {
		t.Fatal(err)
	}
	finish(c)
	request("replacement.example.test", "v2")
	denied, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	blocked, err := pgx.Connect(denied, databaseURL.String())
	if err == nil {
		blocked.Close(ctx)
		t.Fatal("TCP route ignored its source allowlist")
	}
	request("second.example.test", "v1")
	// Reopening durable state after publication replays recovery instead of losing another unit.
	crash := next
	crash.ID = uuid.New()
	crash.RevisionID = uuid.New()
	crash.Images = map[string]string{}
	crash = resolve(crash.WithHash())
	cc := command("deploy", crash, secrets)
	f.engine.Point = func(point string) {
		if point == "route-published" {
			panic("simulated process exit")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("crash point not reached")
			}
		}()
		_ = f.engine.Execute(ctx, cc)
	}()
	if err = f.engine.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = &Engine{Journal: j, Docker: docker, DockerCLI: dockerCLI}
	finish(cc)
	request("second.example.test", "v1")
	nativeControllerTransfer(t, f.engine)
	// The same exported Compose bundle runs with management and the journal closed.
	bundle, err := deployment.ExportConfiguration(crash)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range crash.Specification.Applications {
		for _, id := range crash.InstanceIDs(a.ID) {
			_, err = docker.ContainerRemove(ctx, deployment.UnitName(id), client.ContainerRemoveOptions{Force: true})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err = docker.ContainerRemove(ctx, proxyName, client.ContainerRemoveOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.engine.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "compose.json"), []byte(bundle.Compose), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "routes.yaml"), []byte(bundle.Routes), 0600); err != nil {
		t.Fatal(err)
	}
	// An inherited project name must not redirect startup or teardown.
	composeEnv := append(os.Environ(), "COMPOSE_PROJECT_NAME=unexpected-"+crash.UnitID.String())
	for id, value := range secrets {
		key := "ASTILBA_SECRET_" + strings.ReplaceAll(id, "-", "")
		composeEnv = append(composeEnv, key+"="+value, key+"_URLENCODED="+strings.TrimPrefix(url.UserPassword("", value).String(), ":"))
	}
	composeCommand := func(ctx context.Context, args ...string) *exec.Cmd {
		args = append([]string{"--host", docker.DaemonHost(), "compose", "--project-name", deployment.UnitName(crash.UnitID), "--project-directory", dir, "--file", filepath.Join(dir, "compose.json")}, args...)
		cmd := exec.CommandContext(ctx, dockerCLI, args...)
		cmd.Env = composeEnv
		// Docker launches Compose as a child; cancel both before teardown begins.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = 2 * time.Second
		return cmd
	}
	// Exported ingress has no server label, so Compose must clean up even failed starts.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := composeCommand(cleanup, "down")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("standalone export cleanup: %v %s", err, output)
		}
	})
	startup, cancelStartup := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelStartup()
	run := composeCommand(startup, "up", "--detach", "--wait")
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("standalone export: %v %s", err, output)
	}
	request("replacement.example.test", "v2")
	for _, a := range crash.Specification.Applications {
		for _, id := range crash.InstanceIDs(a.ID) {
			v, err := docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil || !v.Container.State.Running {
				t.Fatal("standalone export did not recreate every instance")
			}
			if v.Container.Config.Labels["com.docker.compose.project"] != deployment.UnitName(crash.UnitID) {
				t.Fatal("standalone export ignored its explicit project identity")
			}
		}
	}
	call, err := docker.ExecCreate(ctx, deployment.UnitName(pg), client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: []string{"psql", "-U", "app", "-d", "app", "-Atc", "SELECT value FROM fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := docker.ExecAttach(ctx, call.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	_, err = stdcopy.StdCopy(&output, &output, stream.Reader)
	stream.Close()
	if err != nil || strings.TrimSpace(output.String()) != "data-survived" {
		t.Fatal("data did not survive replacement, restart and standalone export")
	}
	j, err = Open(state)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = &Engine{Journal: j, Docker: docker, DockerCLI: dockerCLI}
	f.refuseInUseVolumeDeletion(crash)

	// Explicit removal of an in-use volume must fail; deleting a service keeps data.
	inspect, err := docker.VolumeInspect(ctx, deployment.VolumeName(crash.Specification.Volumes[0].ID), client.VolumeInspectOptions{})
	if err != nil || inspect.Volume.Labels[labelServer] != server.String() {
		t.Fatal("data volume lost ownership")
	}
}
