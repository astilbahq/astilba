package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/moby/moby/client"
)

func nativeAbandonReplicaStart(t *testing.T, e *Engine, p deployment.Plan, c api.ExecutionCommand) {
	t.Helper()
	ctx := context.Background()
	secondary := p.InstanceIDs(p.Specification.Applications[0].ID)[1]
	e.Point = func(point string) {
		if point == "visual-service-effect" {
			if _, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(secondary), client.ContainerInspectOptions{}); err == nil {
				panic("interrupted initial pool before readiness and journal commit")
			}
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("initial pool interruption was not reached")
			}
		}()
		_ = e.Execute(ctx, c)
	}()
	e.Point = nil
	c.CancelRequested = true
	if err := e.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	r, err := e.Journal.Record(c.Id)
	if err != nil || r.Report.State != "cancelled" {
		t.Fatal("interrupted initial deployment was not cancelled")
	}
	if err := e.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
}

func nativeReplicaDistribution(t *testing.T, e *Engine, p deployment.Plan) {
	t.Helper()
	ctx := context.Background()
	app := p.Specification.Applications[0]
	host := p.Specification.Routes[0].Domain
	wanted := map[string]bool{}
	for _, id := range p.InstanceIDs(app.ID) {
		v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wanted[v.Container.Config.Hostname] = true
	}
	clientHTTP := &http.Client{Timeout: 2 * time.Second}
	converge := func(expected map[string]bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			seen, good := map[string]bool{}, true
			for range 12 {
				req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1/cgi-bin/identity", nil)
				req.Host = host
				response, err := clientHTTP.Do(req)
				if err != nil {
					good = false
					break
				}
				body, err := io.ReadAll(io.LimitReader(response.Body, 128))
				response.Body.Close()
				id := strings.TrimSpace(string(body))
				if err != nil || response.StatusCode != 200 || !expected[id] {
					good = false
					break
				}
				seen[id] = true
			}
			if good && len(seen) == len(expected) {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatal("proxy did not converge to the exact healthy replica set")
	}
	converge(wanted)
	bad := p.InstanceIDs(app.ID)[1]
	badContainer, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(bad), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.dataOutput(ctx, badContainer.Container.ID, "rm", "/tmp/www/health"); err != nil {
		t.Fatal(err)
	}
	healthy := map[string]bool{}
	for id := range wanted {
		if id != badContainer.Container.Config.Hostname {
			healthy[id] = true
		}
	}
	converge(healthy)
	if _, err = e.dataOutput(ctx, badContainer.Container.ID, "sh", "-c", "printf ready > /tmp/www/health"); err != nil {
		t.Fatal(err)
	}
	converge(wanted)
}
