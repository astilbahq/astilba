package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func TestNativeComposeDeployment(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root in disposable Ubuntu")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if journal != nil {
			journal.Close()
		}
	}()
	server, env, unit, secret := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if err = journal.SetConfig(Config{ServerID: server, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	cli := "docker"
	if _, err := os.Stat("/opt/astilba-plat4/bin/docker"); err == nil {
		config := filepath.Join(state, "docker-cli")
		if err := os.Mkdir(config, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"cliPluginsExtraDirs":["/opt/astilba-plat4/cli-plugins"]}`), 0600); err != nil {
			t.Fatal(err)
		}
		cli = filepath.Join(state, "docker")
		if err := os.WriteFile(cli, []byte(fmt.Sprintf("#!/bin/sh\nexport DOCKER_CONFIG='%s'\nexec /opt/astilba-plat4/bin/docker \"$@\"\n", config)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	engine := &Engine{Journal: journal, Docker: docker, DockerCLI: cli}
	t.Cleanup(func() {
		filter := make(client.Filters).Add("label", labelServer+"="+server.String())
		if list, e := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter}); e == nil {
			for _, v := range list.Items {
				_, _ = docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{Force: true})
			}
		}
		if list, e := docker.NetworkList(ctx, client.NetworkListOptions{Filters: filter}); e == nil {
			for _, v := range list.Items {
				_, _ = docker.NetworkRemove(ctx, v.ID, client.NetworkRemoveOptions{})
			}
		}
		if list, e := docker.VolumeList(ctx, client.VolumeListOptions{Filters: filter}); e == nil {
			for _, v := range list.Items {
				_, _ = docker.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{})
			}
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	source := fmt.Sprintf(`# original source remains authoritative
services:
  db:
    image: %s
    environment:
      POSTGRES_USER: app
      POSTGRES_DB: app
      POSTGRES_PASSWORD_FILE: /run/secrets/password
    secrets: [password]
    volumes: [database:/var/lib/postgresql]
    healthcheck:
      test: [CMD, pg_isready, -U, app, -d, app]
      interval: 1s
      timeout: 2s
      retries: 40
  web:
    image: %s
    command: [sh, -c, 'cat /run/secrets/password; echo retained > /data/marker; exec httpd -f -p 8080 -h /www']
    secrets: [password]
    depends_on:
      db:
        condition: service_healthy
    ports: ['127.0.0.1:%d:8080']
    volumes: [data:/data, './site/index.html:/www/index.html:ro']
volumes:
  database: {}
  data: {}
secrets:
  password:
    environment: DATABASE_PASSWORD
x-keep:
  note: original extension
`, domain.PostgresImage, execution.TestImage, port)
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{{Name: "DATABASE_PASSWORD", SecretVersionID: &secret}}, Compose: &composeproject.Bundle{Namespace: unit, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: source}, {Path: "site/index.html", Content: "first"}}}}
	prepare := func(spec domain.Specification) deployment.Plan {
		t.Helper()
		raw, _ := json.Marshal(spec)
		parsed, _, err := domain.ParseSpec(raw)
		if err != nil {
			t.Fatal(err)
		}
		p := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: env, UnitID: unit, RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: parsed, Images: map[string]string{}, HostChanges: true, AcceptDrift: true, ExpiresAt: time.Now().Add(time.Hour)}
		for _, a := range parsed.Applications {
			p.Images[a.ID.String()] = *a.Source.Image
		}
		return p.WithHash()
	}
	secrets := map[string]string{secret.String(): "source/$password-canary"}
	generation := int64(0)
	command := func(p deployment.Plan, action string) api.ExecutionCommand {
		generation++
		payload := deployment.Payload{Action: action, Plan: p}
		if action != "resolve" {
			payload.Secrets = secrets
		}
		raw, _ := json.Marshal(payload)
		message := json.RawMessage(raw)
		return api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, ResourceId: unit, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: payload.Digest(), Visual: &message, Deadline: time.Now().Add(4 * time.Minute)}
	}
	finish := func(c api.ExecutionCommand) {
		t.Helper()
		if err := engine.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		record, err := journal.Record(c.Id)
		if err != nil || record.Report.State != "succeeded" {
			t.Fatalf("Compose execution: %v %s", err, record.Report.Note)
		}
		if record.Report.Observation == nil {
			t.Fatal("missing runtime observation")
		}
		if strings.Contains(string(*record.Report.Observation), "password-canary") {
			t.Fatal("runtime logs exposed a file secret")
		}
		if err = journal.Acknowledge(c.Id, record.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	request := func(want string) {
		t.Helper()
		response, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if string(body) != want {
			t.Fatalf("file mount: %q", body)
		}
	}
	p := prepare(spec)
	inspection := command(p, "resolve")
	if err := engine.Execute(ctx, inspection); err != nil {
		t.Fatal(err)
	}
	record, _ := journal.Record(inspection.Id)
	var observed deployment.Observation
	if record.Report.Observation == nil || json.Unmarshal(*record.Report.Observation, &observed) != nil {
		t.Fatal("resolve failed")
	}
	journal.Acknowledge(inspection.Id, record.Report.Sequence)
	p.ExpectedState = observed.StateHash
	p = p.WithHash()
	first := command(p, "deploy")
	engine.Point = func(point string) {
		if point == "visual-service-effect" {
			panic("simulated exit")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("restart point not reached")
			}
		}()
		_ = engine.Execute(ctx, first)
	}()
	journal.Close()
	journal, err = Open(state)
	if err != nil {
		t.Fatal(err)
	}
	engine = &Engine{Journal: journal, Docker: docker, DockerCLI: cli}
	finish(first)
	request("first")
	db := deployment.UnitName(composeproject.ResourceID(unit, "service", "db"))
	sql := func(query string) string {
		t.Helper()
		call, err := docker.ExecCreate(ctx, db, client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: []string{"psql", "-U", "app", "-d", "app", "-Atc", query}})
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
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(output.String())
	}
	sql("CREATE TABLE fixture(value text); INSERT INTO fixture VALUES ('retained database');")
	spec.Compose.Files[1].Content = "second"
	next := prepare(spec)
	inspect := command(next, "resolve")
	if err = engine.Execute(ctx, inspect); err != nil {
		t.Fatal(err)
	}
	record, _ = journal.Record(inspect.Id)
	if record.Report.Observation == nil || json.Unmarshal(*record.Report.Observation, &observed) != nil {
		t.Fatal("second resolution failed")
	}
	journal.Acknowledge(inspect.Id, record.Report.Sequence)
	next.ExpectedState = observed.StateHash
	next = next.WithHash()
	finish(command(next, "deploy"))
	request("second")
	if sql("SELECT value FROM fixture") != "retained database" {
		t.Fatal("source edit replaced database storage")
	}
	finish(command(next, "observe"))
	bundle, err := deployment.ExportConfiguration(next)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, file := range bundle.Files {
		name := filepath.Join(dir, file.Path)
		if err = os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(name, []byte(file.Content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(dir, "compose.json"), []byte(bundle.Compose), 0600); err != nil {
		t.Fatal(err)
	}
	for _, app := range next.Specification.Applications {
		if _, err = docker.ContainerRemove(ctx, deployment.UnitName(app.ID), client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	journal.Close()
	journal = nil
	run := exec.Command(cli, "--host", docker.DaemonHost(), "compose", "--project-directory", dir, "--file", filepath.Join(dir, "compose.json"), "up", "--detach", "--wait")
	run.Env = append(os.Environ(), deployment.SecretEnvironmentName(secret)+"="+secrets[secret.String()])
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("standalone source export: %v %s", err, output)
	}
	request("second")
	if sql("SELECT value FROM fixture") != "retained database" {
		t.Fatal("export lost database data")
	}
	t.Cleanup(func() {
		down := exec.Command(cli, "--host", docker.DaemonHost(), "compose", "--project-directory", dir, "--file", filepath.Join(dir, "compose.json"), "down")
		down.Env = run.Env
		_ = down.Run()
	})
}
