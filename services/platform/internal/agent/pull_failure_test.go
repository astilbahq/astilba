package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/moby/moby/client"
)

// refusingPulls is a Docker that has no images and refuses every pull, with the supported Compose
// plugin.
func refusingPulls(t *testing.T, e *Engine) {
	t.Helper()
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/info"), strings.HasSuffix(r.URL.Path, "/images/create"):
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"message":"registry unavailable"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"absent"}`))
		}
	}))
	t.Cleanup(daemon.Close)
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	cli := filepath.Join(t.TempDir(), "docker")
	if err = os.WriteFile(cli, []byte("#!/bin/sh\necho 5.6.1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e.Docker, e.DockerCLI = docker, cli
}

// PLAT-364. A deploy whose images can't be pulled has changed nothing the unit runs, so it fails
// and the host takes the next command. One run again after it began changing the unit's storage
// needs attention, as any later step does.
func TestDeployWhosePullFailsFails(t *testing.T) {
	for _, tc := range []struct {
		name, checkpoint string
		state            api.ExecutionState
	}{
		{name: "first attempt", state: "failed"},
		{name: "run again after its storage step", checkpoint: "storage", state: "needs_attention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, plan := testVisualJournal(t)
			e := &Engine{Journal: j}
			refusingPulls(t, e)
			observed, _, err := e.observeUnit(context.Background(), plan, nil)
			if err != nil {
				t.Fatal(err)
			}
			plan.ExpectedState = observed.StateHash
			plan = plan.WithHash()
			deploy := visualCommand(plan, "deploy", 1)
			if tc.checkpoint != "" {
				if _, err = j.Accept(deploy); err != nil {
					t.Fatal(err)
				}
				if err = e.checkpoint(deploy.Id, tc.checkpoint); err != nil {
					t.Fatal(err)
				}
			}
			if err = e.Execute(context.Background(), deploy); err != nil {
				t.Fatal(err)
			}
			r, err := j.Record(deploy.Id)
			if err != nil || r.Report.State != tc.state || !strings.Contains(r.Report.Note, "was not pulled") {
				t.Fatalf("the deploy ended %s: %s (%v)", r.Report.State, r.Report.Note, err)
			}
			if tc.state != "failed" {
				return
			}
			if err = j.Acknowledge(deploy.Id, r.Report.Sequence); err != nil {
				t.Fatal(err)
			}
			next := visualCommand(plan, "observe", 2)
			if err = e.Execute(context.Background(), next); err != nil {
				t.Fatal(err)
			}
			if r, err = j.Record(next.Id); err != nil || r.Report.State != "succeeded" {
				t.Fatalf("the host did not take the next command: %s %s (%v)", r.Report.State, r.Report.Note, err)
			}
		})
	}
}

// A cross-host preparation whose images can't be pulled has started no candidate, so it fails
// and the deployment aborts. One run again after it began starting candidates, which may have
// replaced a shared service that abort does not restore, needs attention.
func TestCrossHostPreparationWhosePullFails(t *testing.T) {
	for _, tc := range []struct {
		name, checkpoint string
		state            api.ExecutionState
	}{
		{name: "first attempt", state: "failed"},
		{name: "run again after starting candidates", checkpoint: "cross-host-candidates", state: "needs_attention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, body, c := crossHostFixture(t, "prepare")
			if _, err := j.reserveCrossHost(c, body); err != nil {
				t.Fatal(err)
			}
			m := body.Unit.Plan.Local.Network
			raw, _ := stablejson.Marshal(hostnetwork.Snapshot{Manifest: m, Bridges: []hostnetwork.Bridge{}, AppliedHash: hostnetwork.Hash(m)})
			if err := os.MkdirAll(filepath.Join(j.dir, "network"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(j.dir, "network", m.NetworkID.String()+".json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			e := &Engine{Journal: j}
			refusingPulls(t, e)
			if tc.checkpoint != "" {
				if _, err := j.Accept(c); err != nil {
					t.Fatal(err)
				}
				if err := e.checkpoint(c.Id, tc.checkpoint); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Execute(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			r, err := j.Record(c.Id)
			if err != nil || r.Report.State != tc.state || !strings.Contains(r.Report.Note, "was not pulled") {
				t.Fatalf("the preparation ended %s: %s (%v)", r.Report.State, r.Report.Note, err)
			}
		})
	}
}
