package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/moby/moby/api/types/container"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// Runs within the owned native visual fixture, before the same unit's backup
// and PostgreSQL major-upgrade journey. Traffic is sampled independently.
func nativeReleaseJourney(t *testing.T, e *Engine, initial deployment.Plan, secrets map[string]string, resolve func(deployment.Plan) deployment.Plan, command func(string, deployment.Plan, map[string]string) api.ExecutionCommand, finish func(api.ExecutionCommand) Record) deployment.Plan {
	t.Helper()
	ctx := context.Background()
	current := initial
	app := initial.Specification.Applications[0].ID
	host := initial.Specification.Routes[0].Domain
	var mu sync.Mutex
	var failures []string
	var samples int
	samplerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	transport := &http.Transport{DisableKeepAlives: true}
	httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	defer transport.CloseIdleConnections()
	go func() {
		defer close(done)
		for samplerCtx.Err() == nil {
			req, _ := http.NewRequestWithContext(samplerCtx, "GET", "http://127.0.0.1/", nil)
			req.Host = host
			response, err := httpClient.Do(req)
			body := []byte{}
			status := 0
			if err == nil {
				status = response.StatusCode
				body, _ = io.ReadAll(io.LimitReader(response.Body, 128))
				response.Body.Close()
			}
			if samplerCtx.Err() != nil {
				return
			}
			mu.Lock()
			samples++
			if err != nil || status != 200 || !map[string]bool{"v1": true, "v-drained": true, "v2": true, "v3": true, "v4": true, "v5": true, "v6": true}[string(body)] {
				if len(failures) < 20 {
					failures = append(failures, fmt.Sprintf("%d %q %v", status, body, err))
				}
			}
			mu.Unlock()
			select {
			case <-samplerCtx.Done():
				return
			case <-time.After(75 * time.Millisecond):
			}
		}
	}()
	defer func() {
		cancel()
		<-done
		mu.Lock()
		defer mu.Unlock()
		if samples < 10 || len(failures) > 0 {
			t.Errorf("continuous traffic: %d samples, failures %v", samples, failures)
		}
	}()
	prepare := func(value string) deployment.Plan {
		raw, _ := json.Marshal(current)
		var p deployment.Plan
		_ = json.Unmarshal(raw, &p)
		p.ID, p.RevisionID = uuid.New(), uuid.New()
		appliedRevision := current.RevisionID
		p.ExpectedAppliedID = &appliedRevision
		p.HostChanges, p.AcceptDrift = false, false
		p.Release, p.ReleaseOptions = nil, nil
		p.Specification.Applications[0].Variables[0].Value = &value
		count := 3
		if value == "v2" {
			count = 2
		} else if value == "v3" {
			count = 1
		}
		p.Specification.Applications[0].Replicas = count
		p.PrepareInstances(current)
		p = resolve(p.WithHash())
		options := deployment.DefaultReleaseOptions()
		options.DrainSeconds = 1
		p.Release = &deployment.Release{ApplicationID: app, PreviousPlanID: current.ID, Options: options}
		p.ReplaceInstances(app)
		return p.WithHash()
	}
	rejected := func(p deployment.Plan) string {
		t.Helper()
		c := command("deploy", p, secrets)
		if err := e.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		r, err := e.Journal.Record(c.Id)
		if err != nil || r.Report.State != "failed" {
			t.Fatalf("candidate not rejected: %+v %v", r.Report, err)
		}
		if err = e.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		stored, err := e.Journal.unit(p.UnitID)
		if err != nil || stored.Plan.Hash != current.Hash {
			t.Fatal("failed candidate changed active plan")
		}
		for _, id := range p.InstanceIDs(app) {
			if _, err = e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{}); err == nil {
				t.Fatal("rejected candidate member retained")
			}
		}
		return r.Report.Note
	}
	bad := prepare("v-unready")
	bad.Specification.Applications[0].Health.Path = "/missing-readiness"
	bad.Specification.Applications[0].Command[2] = "echo candidate-unready; " + bad.Specification.Applications[0].Command[2]
	t.Log("reject unready candidate")
	if note := rejected(bad.WithHash()); !strings.Contains(note, "candidate-unready") {
		t.Fatal("failed candidate diagnostics lost")
	}
	hookFailure := prepare("v-hook-failure")
	hookFailure.Release.Options.Command = []string{"sh", "-c", `echo migration-failed; echo "$DATABASE_URL"; exit 23`}
	t.Log("reject failed command")
	if note := rejected(hookFailure.WithHash()); !strings.Contains(note, "migration-failed") || !strings.Contains(note, "exit=23") || strings.Contains(note, "canary") {
		t.Fatalf("failed command diagnostics lost or unredacted: %s", note)
	}
	timeout := prepare("v-command-timeout")
	timeout.Release.Options.Command = []string{"sh", "-c", "sleep 3"}
	timeout.Release.Options.CommandTimeoutSeconds = 1
	rejected(timeout.WithHash())
	slowResult := make(chan string, 1)
	slow := prepare("v-drained")
	slow.Release.Options.DrainSeconds = 8
	slow = slow.WithHash()
	e.Point = func(point string) {
		if point == "release-switch" {
			e.Point = nil
			go func() {
				req, _ := http.NewRequest("GET", "http://127.0.0.1/cgi-bin/slow", nil)
				req.Host = host
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					slowResult <- err.Error()
					return
				}
				defer res.Body.Close()
				b, _ := io.ReadAll(io.LimitReader(res.Body, 128))
				slowResult <- string(b)
			}()
			time.Sleep(250 * time.Millisecond)
		}
	}
	finish(command("deploy", slow, secrets))
	e.Point = nil
	select {
	case body := <-slowResult:
		if body != "v1" {
			t.Fatalf("old in-flight connection was not drained: %q", body)
		}
	case <-time.After(time.Second):
		t.Fatal("drained request did not finish")
	}
	current = slow
	restart := func() {
		t.Helper()
		directory := e.Journal.dir
		if err := e.Journal.Close(); err != nil {
			t.Fatal(err)
		}
		j, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		e.Journal = j
		e.Point = nil
	}
	interruptedFailure := prepare("v-failed-cleanup")
	interruptedFailure.Release.Options.Command = []string{"sh", "-c", "echo durable-command-failure; exit 29"}
	interruptedFailure = interruptedFailure.WithHash()
	failureCommand := command("deploy", interruptedFailure, secrets)
	e.Point = func(name string) {
		if name == "release-abort-cleaned" {
			panic("interrupted cleanup")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("cleanup interruption not reached")
			}
		}()
		_ = e.Execute(ctx, failureCommand)
	}()
	restart()
	if err := e.Execute(ctx, failureCommand); err != nil {
		t.Fatal(err)
	}
	failed, err := e.Journal.Record(failureCommand.Id)
	if err != nil || failed.Report.State != "failed" || !strings.Contains(failed.Report.Note, "durable-command-failure") || !strings.Contains(failed.Report.Note, "exit=29") {
		t.Fatalf("cleanup replay lost failure: %+v %v", failed.Report, err)
	}
	if err = e.Journal.Acknowledge(failureCommand.Id, failed.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	for i, point := range []string{"release-prepared", "route-published", "release-route-acknowledged", "release-switch-committed", "release-command-started"} {
		t.Log("interrupt and resume", point)
		p := prepare(fmt.Sprintf("v%d", i+2))
		if point == "release-command-started" {
			p.Release.Options.Command = []string{"sh", "-c", "echo completed"}
			p = p.WithHash()
		}
		c := command("deploy", p, secrets)
		if point == "release-prepared" {
			c.Deadline = time.Now().Add(time.Second)
		}
		hit := false
		e.Point = func(name string) {
			if name == point && !hit {
				hit = true
				panic("native release process interruption")
			}
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("release interruption not reached")
				}
			}()
			_ = e.Execute(ctx, c)
		}()
		restart()
		if point == "release-prepared" && time.Until(c.Deadline) > 0 {
			time.Sleep(time.Until(c.Deadline) + time.Millisecond)
		}
		if point == "release-command-started" {
			before, err := e.Docker.ContainerInspect(ctx, releaseCommandName(c), client.ContainerInspectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			e.Point = func(name string) {
				if name == "release-readiness" {
					after, err := e.Docker.ContainerInspect(ctx, releaseCommandName(c), client.ContainerInspectOptions{})
					if err != nil || before.Container.ID != after.Container.ID || before.Container.State.StartedAt != after.Container.State.StartedAt {
						t.Fatal("completed release command was replayed")
					}
				}
			}
		}
		if !hit {
			t.Fatal("fault was not injected")
		}
		finish(c)
		e.Point = nil
		for _, id := range current.InstanceIDs(app)[1:] {
			if _, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{}); err == nil {
				t.Fatal("retired extra instance retained after drain")
			}
		}
		current = p
		for _, id := range p.InstanceIDs(app) {
			active, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil || !active.Container.State.Running {
				t.Fatal("resumed release did not serve its complete candidate pool")
			}
		}
		metrics := (&MetricCollector{Journal: e.Journal}).Collect(ctx, e.Docker, p.ServerID, e.Journal.dir)
		count := 0
		seen := map[uuid.UUID]bool{}
		for _, resource := range metrics.Services {
			if resource.UnitId == p.UnitID {
				count++
				if seen[resource.ResourceId] {
					t.Fatal("duplicate logical metric")
				}
				seen[resource.ResourceId] = true
			}
		}
		if count != len(p.Specification.Applications) {
			t.Fatalf("candidate/retained metric duplicates: %d", count)
		}
	}
	// An ambiguous start intent never silently runs the command after a restart.
	uncertain := prepare("v-uncertain")
	uncertain.Release.Options.Command = []string{"sh", "-c", "exit 0"}
	uncertain = uncertain.WithHash()
	c := command("deploy", uncertain, secrets)
	e.Point = func(name string) {
		if name == "release-command-intent" {
			panic("lost start acknowledgement")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("hook intent not reached")
			}
		}()
		_ = e.Execute(ctx, c)
	}()
	restart()
	if err := e.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	r, err := e.Journal.Record(c.Id)
	if err != nil || r.Report.State != "needs_attention" {
		t.Fatal("ambiguous hook was replayed")
	}
	c.CancelRequested = true
	if err = e.Journal.Mutate(c.Id, func(r *Record) { r.Report.State = "running" }); err != nil {
		t.Fatal(err)
	}
	if err = e.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	r, _ = e.Journal.Record(c.Id)
	if r.Report.State != "cancelled" {
		t.Fatalf("explicit cancellation did not recover: %+v", r.Report)
	}
	if err = e.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	gateName := "astilba-release-gate-" + current.UnitID.String()
	gateScript := `mkdir -p /tmp/gate/cgi-bin; printf '#!/bin/sh\nif [ -f /tmp/ready ]; then printf "Content-Type: text/plain\\r\\n\\r\\nready"; else printf "Status: 503\\r\\n\\r\\nmissing"; fi\n' > /tmp/gate/cgi-bin/ready; printf '#!/bin/sh\ntouch /tmp/ready\nprintf "Content-Type: text/plain\\r\\n\\r\\nmigrated"\n' > /tmp/gate/cgi-bin/migrate; chmod +x /tmp/gate/cgi-bin/*; exec httpd -f -p 8080 -h /tmp/gate`
	gate, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: gateName, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sh", "-c", gateScript}, Labels: map[string]string{labelServer: current.ServerID.String(), labelKind: "release-fixture-v1"}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(deployment.NetworkName(current.UnitID))}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Docker.ContainerStart(ctx, gate.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	migration := prepare("v6")
	migration.Specification.Applications[0].Command[2] = "wget -q --spider http://" + gateName + ":8080/cgi-bin/ready || exit 31; " + migration.Specification.Applications[0].Command[2]
	migration.Release.Options.Command = []string{"wget", "-qO-", "http://" + gateName + ":8080/cgi-bin/migrate"}
	migration = migration.WithHash()
	finish(command("deploy", migration, secrets))
	current = migration
	// Explicit rollback uses the retained immutable image and old application
	// configuration, through a new candidate and the same readiness/switch path.
	rollback := prepare("v1")
	rollback.Specification.Applications[0] = initial.Specification.Applications[0]
	rollback.PrepareInstances(current)
	rollback.ReplaceInstances(app)
	rollback.Release.Options.Command = []string{}
	rollback = rollback.WithHash()
	finish(command("deploy", rollback, secrets))
	current = rollback
	if _, err = e.Docker.ContainerRemove(ctx, gate.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	retained, err := e.Journal.release(current.UnitID)
	if err != nil || len(retained.Retained) != 2 {
		t.Fatalf("retention is not bounded: %+v %v", retained, err)
	}
	cancel()
	<-done
	// Ordinary reconciliation after a protected release must address the active
	// instance, and the following database upgrade must quiesce that same writer.
	current.ID, current.RevisionID = uuid.New(), uuid.New()
	current.Release = nil
	current = resolve(current.WithHash())
	finish(command("deploy", current, secrets))
	raw, _ := json.Marshal(current)
	var converted deployment.Plan
	_ = json.Unmarshal(raw, &converted)
	volume := uuid.New()
	pgimage := domain.Postgres17Image
	passwordID := current.Specification.Applications[1].Postgres.PasswordSecretVersionID
	converted.ID, converted.RevisionID = uuid.New(), uuid.New()
	convertedAppliedRevision := current.RevisionID
	converted.ExpectedAppliedID = &convertedAppliedRevision
	converted.Specification.Applications[0] = domain.Application{ID: app, Name: "Converted database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgimage}, Postgres: &domain.Postgres{Database: "converted", Username: "app", PasswordSecretVersionID: passwordID}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(pgimage)}}}
	converted.Specification.Volumes = append(converted.Specification.Volumes, domain.Volume{ID: volume, Name: "Conversion data"})
	converted.Specification.Bindings = nil
	converted.Specification.Routes = nil
	delete(converted.Instances, app.String())
	converted = resolve(converted.WithHash())
	finish(command("deploy", converted, secrets))
	resources, err := e.ownedContainers(ctx, converted)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, r := range resources {
		if r.Labels[labelResource] == app.String() {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("kind conversion retained obsolete instances: %d", count)
	}
	current.ID, current.RevisionID = uuid.New(), uuid.New()
	backAppliedRevision := converted.RevisionID
	current.ExpectedAppliedID = &backAppliedRevision
	delete(current.Instances, app.String())
	current = resolve(current.WithHash())
	finish(command("deploy", current, secrets))
	mapped := prepare("v-restored")
	finish(command("deploy", mapped, secrets))
	current = mapped
	return current
}
