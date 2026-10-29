package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

type maintenanceContinuationFixture struct {
	journal    *Journal
	engine     *Engine
	command    api.ExecutionCommand
	body       deployment.CrossHostCommand
	descriptor deployment.RecoveryDescriptor
}

func expiredMaintenance(t *testing.T, reserved bool) maintenanceContinuationFixture {
	t.Helper()
	j, p := testVisualJournal(t)
	cfg, _ := j.Config()
	cfg.ControllerGeneration = 1
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	app, remote, network := uuid.New(), uuid.New(), uuid.New()
	image := "nginx:stable"
	p.Recipe = deployment.CrossHostRecipe
	p.Specification.Applications = []domain.Application{{ID: app, Name: "Remote", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
	p.Specification.Routes = []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "maintenance.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}
	p.Specification.Placement = &domain.Placement{NetworkID: network, IngressServerID: p.ServerID, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 100, MemoryMiB: 64, Targets: []domain.PlacementTarget{{ServerID: remote, Count: 1}}}}}
	p.Local = &deployment.LocalPlacement{GroupID: uuid.New(), Primary: true, Instances: map[string][]uuid.UUID{}, Network: hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: uuid.New(), ServerID: p.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ListenPort: 51820}}
	p = p.WithHash()
	if err := p.Validate(true); err != nil {
		t.Fatal("invalid maintenance fixture plan", err)
	}
	if err := j.saveUnit(unitState{Plan: p, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	parent, id := uuid.New(), uuid.New()
	envelope := []byte("opaque-frozen-maintenance-root")
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: p.Local.GroupID, GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(envelope), Bytes: len(envelope), Participants: []uuid.UUID{p.ServerID}}
	body := deployment.CrossHostCommand{ParentID: parent, GroupHash: d.RootHash, Phase: "resume-source", Unit: deployment.DistributedUnit{Plan: p}, Maintenance: &deployment.Maintenance{Kind: "backup", ResourceID: uuid.New(), RunID: uuid.New(), EffectHash: strings.Repeat("c", 64), ExpectedState: strings.Repeat("d", 64)}}
	raw, _ := json.Marshal(body)
	message := json.RawMessage(raw)
	descriptor, _ := json.Marshal(d)
	metadata := json.RawMessage(descriptor)
	generation := int64(1)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: 1, ControllerGeneration: &generation, PlanHash: body.Digest(), Deadline: time.Now().Add(-time.Hour), Crosshost: &message, RecoveryCheckpoint: &metadata}
	if err := j.retainCheckpoint(c, d, envelope); err != nil {
		t.Fatal(err)
	}
	if reserved {
		if _, err := j.reserveCrossHost(c, body); err != nil {
			t.Fatal(err)
		}
	}
	// This primary has no local application writers. Any Docker mutation would
	// violate the no-effect continuation and unreserved acknowledgment paths.
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/astilba-ingress/json") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"absent"}`))
			return
		}
		t.Errorf("unexpected host effect: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}))
	t.Cleanup(daemon.Close)
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return maintenanceContinuationFixture{journal: j, engine: &Engine{Journal: j, Docker: docker}, command: c, body: body, descriptor: d}
}

func (f *maintenanceContinuationFixture) freezeBody() {
	raw, _ := json.Marshal(f.body)
	message := json.RawMessage(raw)
	f.command.Crosshost, f.command.PlanHash = &message, f.body.Digest()
}

func TestExpiredMaintenanceFirstDeliveryReconcilesAndPreservesTerminalResult(t *testing.T) {
	for _, reserved := range []bool{true, false} {
		t.Run(map[bool]string{true: "reserved", false: "unreserved-source"}[reserved], func(t *testing.T) {
			f := expiredMaintenance(t, reserved)
			f.command.CancelRequested = true
			if err := f.engine.Execute(context.Background(), f.command); err != nil {
				t.Fatal(err)
			}
			r, err := f.journal.Record(f.command.Id)
			if err != nil || r.Report.State != "succeeded" || !r.Command.Deadline.Equal(f.command.Deadline) {
				t.Fatal("delayed writer resumption was cancelled or replaced", r.Report.State, err)
			}
			before := deployment.Hash(r.Report)
			if err = f.engine.Execute(context.Background(), f.command); err != nil {
				t.Fatal(err)
			}
			r, _ = f.journal.Record(f.command.Id)
			if deployment.Hash(r.Report) != before {
				t.Fatal("terminal resumption was reset or re-executed")
			}
			state, err := f.journal.crossHost()
			if err != nil || reserved && state.Phase != "resumed" || !reserved && state.ParentID != uuid.Nil {
				t.Fatal("no-effect continuation changed reservation ownership", err)
			}
		})
	}
}

func TestExpiredMaintenanceKeepsUncertainPrimaryStopped(t *testing.T) {
	f := expiredMaintenance(t, true)
	target := f.body.Unit.Plan
	target.ID = uuid.New()
	target = target.WithHash()
	f.body.Maintenance.Kind, f.body.Maintenance.Target = "upgrade", &target
	f.body.Phase = "resume-target"
	f.freezeBody()
	s, _ := f.journal.crossHost()
	s.Maintenance, s.Phase, s.EffectAction = f.body.Maintenance, "effect", "cutover"
	if err := f.journal.saveCrossHost(s); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.saveUpgrade(upgradeState{Command: upgrade.Command{Plan: upgrade.Plan{ID: s.Maintenance.ResourceID}}, Status: upgrade.Status{Phase: "ready"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Execute(context.Background(), f.command); err != nil {
		t.Fatal(err)
	}
	r, err := f.journal.Record(f.command.Id)
	if err != nil || r.Report.State != "needs_attention" || !strings.Contains(r.Report.Note, "no confirmed result") {
		t.Fatal("expired continuation manufactured a primary outcome", r.Report, err)
	}
	s, _ = f.journal.crossHost()
	if s.Phase != "effect" || s.ResumeTarget != nil {
		t.Fatal("uncertain primary changed writer recovery state")
	}
}

func TestExpiredMaintenanceRejectsChangedAuthorityRootAndTarget(t *testing.T) {
	for _, change := range []string{"controller", "parent", "root", "target"} {
		t.Run(change, func(t *testing.T) {
			f := expiredMaintenance(t, true)
			switch change {
			case "controller":
				f.command.ControllerGeneration = nil
			case "parent":
				parent := uuid.New()
				f.command.ParentId, f.body.ParentID = &parent, parent
				f.freezeBody()
			case "root":
				d := f.descriptor
				d.RootHash = strings.Repeat("e", 64)
				raw, _ := json.Marshal(d)
				metadata := json.RawMessage(raw)
				f.command.RecoveryCheckpoint = &metadata
			case "target":
				target := f.body.Unit.Plan
				target.ID = uuid.New()
				target = target.WithHash()
				f.body.Maintenance.Kind, f.body.Maintenance.Target, f.body.Phase = "upgrade", &target, "resume-target"
				f.freezeBody()
			}
			_ = f.engine.Execute(context.Background(), f.command)
			s, err := f.journal.crossHost()
			if err != nil || s.Phase != "reserved" || s.ResumeTarget != nil {
				t.Fatal("changed continuation touched the frozen reservation", err)
			}
			if r, err := f.journal.Record(f.command.Id); err == nil && r.Report.State != "needs_attention" {
				t.Fatal("changed continuation did not fail closed", r.Report)
			}
		})
	}
}

func TestExpiredMaintenanceAcceptsOlderRetainedCheckpointInSameRoot(t *testing.T) {
	f := expiredMaintenance(t, true)
	body := []byte("later-frozen-phase")
	next := f.descriptor
	next.Sequence, next.PreviousHash, next.CheckpointID = 1, next.Digest, uuid.New()
	next.Bytes, next.Digest = len(body), deployment.RecoveryDigest(body)
	if err := f.journal.retainCheckpoint(f.command, next, body); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Execute(context.Background(), f.command); err != nil {
		t.Fatal(err)
	}
	r, err := f.journal.Record(f.command.Id)
	if err != nil || r.Report.State != "succeeded" {
		t.Fatal("older frozen same-root child could not reconcile", err)
	}
	head, _ := f.journal.recoveryHead(f.body.ParentID)
	if head.Descriptor.Sequence != 1 {
		t.Fatal("older replay lowered retained recovery knowledge")
	}
}

func TestOlderControllerCannotStartMaintenanceOnMigratedAgent(t *testing.T) {
	f := expiredMaintenance(t, true)
	c := NewClient(f.journal, f.engine)
	c.crossHost.Store(true)
	if _, err := c.acceptPolledCommand(f.command); err == nil {
		t.Fatal("older controller started maintenance using only crossHost")
	}
	if _, err := f.journal.Record(f.command.Id); !os.IsNotExist(err) {
		t.Fatal("unsupported maintenance entered the journal", err)
	}
	c.maintenanceContinuation.Store(true)
	if _, err := c.acceptPolledCommand(f.command); err != nil {
		t.Fatal(err)
	}
	c.maintenanceContinuation.Store(false)
	if _, err := c.acceptPolledCommand(f.command); err != nil {
		t.Fatal("controller downgrade suppressed exact accepted reconciliation", err)
	}
	changed := f.command
	changed.Deadline = changed.Deadline.Add(time.Hour)
	if _, err := c.acceptPolledCommand(changed); err == nil {
		t.Fatal("unsupported controller changed an accepted command identity")
	}
	f.command.Id, f.command.Generation, f.body.Phase = uuid.New(), 2, "pause"
	f.freezeBody()
	if _, err := c.acceptPolledCommand(f.command); err == nil {
		t.Fatal("older controller delivered a new maintenance phase")
	}
}
