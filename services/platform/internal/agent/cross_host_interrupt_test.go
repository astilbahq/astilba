package agent

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// A SIGTERM cancels the context of the phase in progress. An error that causes is not the
// phase's outcome: the record must stay running, so a restarted agent reruns the phase.
// TestNativeCrossHostDeployment reruns these against real hosts.
func TestStoppingAgentRecordsNoCrossHostOutcome(t *testing.T) {
	retiring := time.Now().Add(-time.Minute)
	for _, test := range []struct {
		name, phase string
		reserved    func(*crossHostState)
	}{
		{name: "resolve", phase: "resolve"},
		{name: "observe", phase: "observe"},
		{name: "reserve before the reservation", phase: "reserve"},
		{name: "reserve after the reservation", phase: "reserve", reserved: func(*crossHostState) {}},
		{name: "prepare", phase: "prepare", reserved: func(*crossHostState) {}},
		{name: "probe", phase: "probe", reserved: func(s *crossHostState) { s.Phase = "prepared" }},
		{name: "publish", phase: "publish", reserved: func(s *crossHostState) { s.Phase = "probed" }},
		{name: "publish after acknowledgement", phase: "publish", reserved: func(s *crossHostState) { s.Phase, s.Publication = "published", true }},
		{name: "retire", phase: "retire", reserved: func(s *crossHostState) { s.Phase, s.Publication, s.DrainUntil = "published", true, retiring }},
		{name: "complete", phase: "complete", reserved: func(s *crossHostState) { s.Phase = "retired" }},
		{name: "abort", phase: "abort", reserved: func(*crossHostState) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			j, p := testVisualJournal(t)
			image, app, network := execution.TestImage, uuid.New(), uuid.New()
			p.Recipe = deployment.CrossHostRecipe
			p.Images = map[string]string{app.String(): image}
			p.Specification.Applications = []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
			p.Specification.Routes = []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "interrupt.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}
			p.Specification.Placement = &domain.Placement{NetworkID: network, IngressServerID: p.ServerID, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 100, MemoryMiB: 64, Targets: []domain.PlacementTarget{{ServerID: p.ServerID, Count: 1}}}}}
			instance := uuid.New()
			p.Local = &deployment.LocalPlacement{GroupID: uuid.New(), Primary: true, Instances: map[string][]uuid.UUID{app.String(): {instance}}, Requests: map[string]deployment.ResourceRequest{app.String(): {CPUMillis: 100, MemoryMiB: 64}}, Backends: map[string][]deployment.PrivateEndpoint{app.String(): {{ServerID: p.ServerID, UnitID: p.UnitID, ResourceID: app, InstanceID: instance, Address: deployment.ServiceName(instance), Port: 8080, ContainerPort: 8080}}}, Network: hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: uuid.New(), ServerID: p.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ListenPort: 51820}}
			p = p.WithHash()
			if err := p.Validate(true); err != nil {
				t.Fatal("invalid fixture plan: ", err)
			}
			previous := p
			previous.Local = &deployment.LocalPlacement{Instances: map[string][]uuid.UUID{app.String(): {uuid.New()}}}
			if err := j.saveUnit(unitState{Plan: p, Expected: map[string]string{}}); err != nil {
				t.Fatal(err)
			}
			parent, first := uuid.New(), uuid.New()
			hash := strings.Repeat("b", 64)
			envelope := []byte("opaque-frozen-deployment-root")
			d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: hash, GroupID: p.Local.GroupID, GroupHash: hash, BaseID: first, CheckpointID: first, Digest: deployment.RecoveryDigest(envelope), Bytes: len(envelope), Participants: []uuid.UUID{p.ServerID}}
			descriptor, _ := stablejson.Marshal(d)
			metadata := jsontext.Value(descriptor)
			body := deployment.CrossHostCommand{ParentID: parent, GroupHash: hash, Phase: test.phase, Unit: deployment.DistributedUnit{Plan: p, Previous: previous}}
			raw, _ := stablejson.Marshal(body)
			message := jsontext.Value(raw)
			c := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: 2, PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Crosshost: &message, RecoveryCheckpoint: &metadata}
			if err := j.retainCheckpoint(c, d, envelope); err != nil {
				t.Fatal(err)
			}
			if test.reserved != nil {
				s, err := j.reserveCrossHost(c, body)
				if err != nil {
					t.Fatal(err)
				}
				test.reserved(&s)
				if err = j.saveCrossHost(s); err != nil {
					t.Fatal(err)
				}
			}
			docker, err := client.New(client.WithHost("tcp://127.0.0.1:1"), client.WithAPIVersion("1.54"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { docker.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err = (&Engine{Journal: j, Docker: docker}).Execute(ctx, c); err == nil {
				t.Fatal("a cancelled phase reported no error, so the host would not retry it")
			}
			r, err := j.Record(c.Id)
			if err != nil {
				t.Fatal(err)
			}
			if r.Report.State != "running" {
				t.Fatalf("stopping recorded %s: %s", r.Report.State, r.Report.Note)
			}
		})
	}
}

// The private links a deployment installs between its reserve and prepare phases.
func TestStoppingAgentRecordsNoNetworkOutcome(t *testing.T) {
	j, p := testVisualJournal(t)
	if err := os.WriteFile(filepath.Join(j.dir, "network-enabled"), []byte("astilba-managed-network-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := hostnetwork.Manifest{Version: 1, NetworkID: uuid.New(), MembershipID: uuid.New(), ServerID: p.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ListenPort: 51820}
	body := hostnetwork.Command{Action: "configure", NetworkID: m.NetworkID, MembershipID: m.MembershipID, Manifest: &m}
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, Generation: 1, Kind: "network", Action: "inspect", ResourceId: m.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(time.Hour), Network: &message}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Engine{Journal: j}).Execute(ctx, c); err == nil {
		t.Fatal("a cancelled phase reported no error, so the host would not retry it")
	}
	r, err := j.Record(c.Id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Report.State != "running" {
		t.Fatalf("stopping recorded %s: %s", r.Report.State, r.Report.Note)
	}
}

// Failing to run a probe says nothing about the writer, and must not excuse it from readiness.
func TestReservationDoesNotBlameAWriterForAProbeThatCouldNotRun(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"/containers/create", "/start"} {
		t.Run(failing, func(t *testing.T) {
			t.Parallel()
			j, p := testVisualJournal(t)
			image, app := execution.TestImage, uuid.New()
			p.Images = map[string]string{app.String(): image}
			p.Specification.Applications = []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, failing):
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"cannot run a probe"}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"Id":"probe"}`))
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				case strings.Contains(r.URL.Path, "astilba-health-"):
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message":"no such container"}`))
				default:
					_, _ = w.Write([]byte(`{"Id":"writer","State":{"Running":true}}`))
				}
			}))
			t.Cleanup(daemon.Close)
			docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { docker.Close() })
			s := crossHostState{Unit: deployment.DistributedUnit{Plan: p}}
			if err = (&Engine{Journal: j, Docker: docker}).recordUnready(context.Background(), api.ExecutionCommand{Id: uuid.New(), ResourceId: p.UnitID}, &s); err == nil {
				t.Fatal("a probe that could not run was reported as a failing writer")
			}
			if len(s.Unready) != 0 {
				t.Fatalf("a writer was excused from readiness: %v", s.Unready)
			}
		})
	}
}
