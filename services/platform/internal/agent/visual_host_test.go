package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// Run only on the disposable native Linux host: this fixture publishes ports.
func TestNativeVisualDeployment(t *testing.T) {
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
	defer docker.Close()
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
	defer func() {
		if j != nil {
			j.Close()
		}
	}()
	server := uuid.New()
	if err = j.SetConfig(Config{ServerID: server, MachineID: uuid.New(), Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	dockerCLI := "docker"
	// CI installs its pinned toolchain separately from the runner's Docker.
	if _, err := os.Stat("/opt/astilba-plat4/bin/docker"); err == nil {
		config := filepath.Join(state, "fixture-docker")
		if err = os.Mkdir(config, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"cliPluginsExtraDirs":["/opt/astilba-plat4/cli-plugins"]}`), 0600); err != nil {
			t.Fatal(err)
		}
		dockerCLI = filepath.Join(state, "docker")
		script := fmt.Sprintf("#!/bin/sh\nexport DOCKER_CONFIG='%s'\nexec /opt/astilba-plat4/bin/docker \"$@\"\n", config)
		if err = os.WriteFile(dockerCLI, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	engine := &Engine{Journal: j, Docker: docker, DockerCLI: dockerCLI}
	t.Cleanup(func() {
		filter := make(client.Filters).Add("label", labelServer+"="+server.String())
		if all, e := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter}); e == nil {
			for _, v := range all.Items {
				_, _ = docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
			}
		}
		if all, e := docker.NetworkList(ctx, client.NetworkListOptions{Filters: filter}); e == nil {
			for _, v := range all.Items {
				_, _ = docker.NetworkRemove(ctx, v.ID, client.NetworkRemoveOptions{})
			}
		}
		if all, e := docker.VolumeList(ctx, client.VolumeListOptions{Filters: filter}); e == nil {
			for _, v := range all.Items {
				_, _ = docker.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{})
			}
		}
	})
	generation := int64(0)
	command := func(action string, p deployment.Plan, secrets map[string]string) api.ExecutionCommand {
		generation++
		payload := deployment.Payload{Action: action, Plan: p, Secrets: secrets}
		raw, _ := json.Marshal(payload)
		v := json.RawMessage(raw)
		return api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "visual", Action: "inspect", Generation: generation, PlanHash: payload.Digest(), Visual: &v, Deadline: time.Now().Add(time.Minute)}
	}
	finish := func(c api.ExecutionCommand) Record {
		t.Helper()
		if err := engine.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		r, err := engine.Journal.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Report.State != "succeeded" {
			t.Fatalf("%s failed: %s", c.Kind, r.Report.Note)
		}
		if err = engine.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		return r
	}
	plan := func(name string, withDB bool) (deployment.Plan, map[string]string) {
		app, pg, vol, secret := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		image := execution.TestImage
		pgimage := domain.PostgresImage
		value := "v1"
		spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "VALUE", Value: &value}}, Command: []string{"sh", "-c", "mkdir -p /tmp/www; printf '%s' \"$VALUE\" > /tmp/www/index.html; echo \"$DATABASE_URL\"; exec httpd -f -p 8080 -h /tmp/www"}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: name + ".example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
		secrets := map[string]string{}
		if withDB {
			spec.Volumes = []domain.Volume{{ID: vol, Name: "Data"}}
			spec.Applications = append(spec.Applications, domain.Application{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgimage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: "/var/lib/postgresql"}}})
			spec.Bindings = []domain.Binding{{ID: uuid.New(), SourceID: pg, TargetID: app, Variable: "DATABASE_URL"}}
			secrets[secret.String()] = "native/$secret@canary"
			spec.Routes = append(spec.Routes, domain.Route{ID: uuid.New(), TargetID: pg, Protocol: "tcp", Domain: "db.example.test", Port: 5432, ListenPort: 15432, TLS: "off", AllowCIDRs: []string{"0.0.0.0/0"}})
		}
		p := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{}, HostChanges: true, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
		return p, secrets
	}
	resolve := func(p deployment.Plan) deployment.Plan {
		t.Helper()
		r := finish(command("resolve", p, nil))
		var observed deployment.Observation
		if err := json.Unmarshal(*r.Report.Observation, &observed); err != nil {
			t.Fatal(err)
		}
		p.Images = observed.Images
		p.ExpectedState = observed.StateHash
		return p.WithHash()
	}
	request := func(host, want string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://127.0.0.1/", nil)
		req.Host = host
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		if res.StatusCode != 200 || string(b) != want {
			t.Fatalf("route %s: %d %q", host, res.StatusCode, b)
		}
	}
	p, secrets := plan("first", true)
	p = resolve(p)
	r := finish(command("deploy", p, secrets))
	request("first.example.test", "v1")
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
		collector := &MetricCollector{}
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
	t.Run("catalog", func(t *testing.T) { nativeCatalogJourney(t, engine, server, command) })
	db, err := pgx.Connect(ctx, databaseURL.String())
	if err != nil {
		t.Fatal("TCP route did not connect to PostgreSQL")
	}
	if _, err = db.Exec(ctx, "CREATE TABLE fixture(value text); INSERT INTO fixture VALUES ('data-survived')"); err != nil {
		t.Fatal(err)
	}
	db.Close(ctx)
	t.Run("database_metadata_restore", func(t *testing.T) {
		nativePostgresMetadataRestore(t, engine, p)
	})
	t.Run("backup_and_fresh_restore", func(t *testing.T) {
		nativeBackupJourney(t, engine, p, func(b backup.Command) Record {
			generation++
			raw, _ := json.Marshal(b)
			value := json.RawMessage(raw)
			return finish(api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: p.EnvironmentID, ResourceId: b.Installation.Policy.ID, Kind: "backup", Action: "inspect", Generation: generation, PlanHash: b.Digest(), Backup: &value, Deadline: time.Now().Add(time.Minute)})
		})
	})
	t.Run("supported_major_upgrades", func(t *testing.T) {
		releaseVerified := false
		nativeUpgradeJourney(t, engine, func(t *testing.T) (deployment.Plan, map[string]string) {
			p, secrets := plan("upgrade-"+uuid.NewString(), true)
			p.Specification.Routes = p.Specification.Routes[:1]
			image := domain.Postgres17Image
			p.Specification.Applications[1].Source.Image = &image
			p.Specification.Applications[1].Mounts[0].Target = domain.PostgresMount(image)
			p = p.WithHash()
			if !releaseVerified {
				p.Specification.Applications[0].Command = []string{"sh", "-c", `mkdir -p /tmp/www/cgi-bin; printf '%s' "$VALUE" > /tmp/www/index.html; printf '#!/bin/sh\nsleep 6\nprintf "Content-Type: text/plain\\r\\n\\r\\n"\nprintf "%%s" "$VALUE"\n' > /tmp/www/cgi-bin/slow; chmod +x /tmp/www/cgi-bin/slow; exec httpd -f -p 8080 -h /tmp/www`}
				p = p.WithHash()
			}
			p = resolve(p)
			finish(command("deploy", p, secrets))
			if !releaseVerified {
				p = nativeReleaseJourney(t, engine, p, secrets, resolve, command, finish)
				releaseVerified = true
				j = engine.Journal
			}
			return p, secrets
		}, func(cmd upgrade.Command) api.ExecutionCommand {
			generation++
			raw, _ := json.Marshal(cmd)
			value := json.RawMessage(raw)
			return api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: cmd.Plan.Source.EnvironmentID, ResourceId: cmd.Plan.ID, Kind: "upgrade", Action: "inspect", Generation: generation, PlanHash: cmd.Digest(), Upgrade: &value, Deadline: time.Now().Add(time.Hour)}
		})
		j = engine.Journal
	})
	nativeFilesAndJobs(t, engine, server)
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
	engine.Point = func(point string) {
		if point == "route-publishing" && !injected {
			if _, err := docker.ContainerStop(ctx, proxyName, client.ContainerStopOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if point == "route-published" && !injected {
			injected = true
			if err := os.WriteFile(engine.routePath(), []byte("{invalid"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = engine.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	r, err = j.Record(c.Id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Report.State != "needs_attention" || !strings.Contains(r.Report.Note, "restored") {
		t.Fatalf("reload was not rejected and recovered: %s", r.Report.Note)
	}
	request("first.example.test", "v2")
	request("second.example.test", "v1")
	engine.Point = nil
	if err = j.Mutate(c.Id, func(r *Record) { r.Report.State = "running" }); err != nil {
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
	engine.Point = func(point string) {
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
		_ = engine.Execute(ctx, cc)
	}()
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j = nil
	j, err = Open(state)
	if err != nil {
		t.Fatal(err)
	}
	engine = &Engine{Journal: j, Docker: docker, DockerCLI: dockerCLI}
	finish(cc)
	request("second.example.test", "v1")
	j = nativeControllerTransfer(t, engine)
	// The same exported Compose bundle runs with management and the journal closed.
	bundle, err := deployment.ExportConfiguration(crash)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range crash.Specification.Applications {
		_, err = docker.ContainerRemove(ctx, deployment.UnitName(a.ID), client.ContainerRemoveOptions{Force: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = docker.ContainerRemove(ctx, proxyName, client.ContainerRemoveOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j = nil
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "compose.json"), []byte(bundle.Compose), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "routes.yaml"), []byte(bundle.Routes), 0600); err != nil {
		t.Fatal(err)
	}
	run := exec.Command(dockerCLI, "--host", docker.DaemonHost(), "compose", "--project-directory", dir, "--file", filepath.Join(dir, "compose.json"), "up", "--detach", "--wait")
	run.Env = os.Environ()
	for id, value := range secrets {
		key := "ASTILBA_SECRET_" + strings.ReplaceAll(id, "-", "")
		run.Env = append(run.Env, key+"="+value, key+"_URLENCODED="+strings.TrimPrefix(url.UserPassword("", value).String(), ":"))
	}
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("standalone export: %v %s", err, output)
	}
	request("replacement.example.test", "v2")
	t.Cleanup(func() {
		cmd := exec.Command(dockerCLI, "--host", docker.DaemonHost(), "compose", "--project-directory", dir, "--file", filepath.Join(dir, "compose.json"), "down")
		cmd.Env = run.Env
		_ = cmd.Run()
	})
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
	engine = &Engine{Journal: j, Docker: docker, DockerCLI: dockerCLI}
	deletion := command("delete-volume", crash, nil)
	controllerGeneration := int64(1)
	deletion.ControllerGeneration = &controllerGeneration
	var payload deployment.Payload
	_ = json.Unmarshal(*deletion.Visual, &payload)
	volumeID := crash.Specification.Volumes[0].ID
	payload.VolumeID = &volumeID
	raw, _ := json.Marshal(payload)
	message := json.RawMessage(raw)
	deletion.Visual = &message
	deletion.PlanHash = payload.Digest()
	if err = engine.Execute(ctx, deletion); err != nil {
		t.Fatal(err)
	}
	deleted, err := j.Record(deletion.Id)
	if err != nil || deleted.Report.State != "needs_attention" {
		t.Fatal("in-use volume deletion was not refused")
	}

	// Explicit removal of an in-use volume must fail; deleting a service keeps data.
	inspect, err := docker.VolumeInspect(ctx, deployment.VolumeName(crash.Specification.Volumes[0].ID), client.VolumeInspectOptions{})
	if err != nil || inspect.Volume.Labels[labelServer] != server.String() {
		t.Fatal("data volume lost ownership")
	}
}
