package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
)

// Packet reachability and installed-agent barriers are separate native gates.
// This fixture proves tenant admission, frozen per-host authority and durable
// parent/child transitions through the real PostgreSQL/River execution service.
type coordinatedScheduleSweep struct{}

func (coordinatedScheduleSweep) Kind() string { return "platform_coordinated_schedule_v1" }

func TestCrossHostCoordinatedPlacement(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", base+"/projects", map[string]string{"name": "Placement"}, 201, &project)
	var env api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Web"}, 201, &env)
	path := base + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	inventory := hostnetwork.StableInventory(nil)
	servers := []api.Enrollment{}
	identities := []execution.Identity{}
	enrollments := []api.AgentEnrollment{}
	members := []api.ManagedNetworkMemberInput{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	var holdRemotePause atomic.Bool
	var holdRemoteReserve atomic.Bool
	var holdRemoteResume atomic.Bool
	var failCandidate atomic.Bool
	var revokePrimary atomic.Bool
	var holdCheckpoint atomic.Bool
	var holdClosure atomic.Bool
	seen := sync.Map{}
	unitPlans := sync.Map{}
	for i := 0; i < 2; i++ {
		enrolled, in, token := enrollment(t, f, b, ws, "deployment")
		yes := true
		in.Capabilities.Backups = &yes
		in.Capabilities.Upgrades = &yes
		in.Capabilities.Visual = &yes
		in.Capabilities.SafeReleases = &yes
		in.Capabilities.CrossHost = &yes
		in.Capabilities.Replicas = &yes
		in.Capabilities.ServiceOutputs = &yes
		in.Preflight.CrossHostInventory = &api.CrossHostInventory{IngressReady: &yes, CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: inventory}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		identity, err := f.execution.Authenticate(ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		servers = append(servers, enrolled)
		identities = append(identities, identity)
		enrollments = append(enrollments, in)
		members = append(members, api.ManagedNetworkMemberInput{ServerId: enrolled.Server.Id, Endpoint: []string{"192.0.2.1:51820", "192.0.2.2:51820"}[i], ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: inventory})
		host := hostnetwork.Host{Dir: t.TempDir()}
		wg.Go(func() {
			lastHeartbeat := time.Time{}
			for ctx.Err() == nil {
				if time.Since(lastHeartbeat) >= 15*time.Second {
					if _, err := f.execution.Heartbeat(ctx, identity, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}); err != nil {
						if i == 0 && revokePrimary.Load() && errors.Is(err, domain.Unauthenticated) {
							return
						}
						if ctx.Err() == nil {
							t.Error(err)
						}
						return
					}
					lastHeartbeat = time.Now()
				}
				poll, done := context.WithTimeout(ctx, time.Second)
				c, err := f.execution.Poll(poll, identity)
				pollErr := poll.Err()
				done()
				if err != nil {
					if i == 0 && revokePrimary.Load() && errors.Is(err, domain.Unauthenticated) {
						return
					}
					if pollErr != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
						continue
					}
					t.Error(err)
					return
				}
				if c == nil {
					continue
				}
				report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
				if c.Kind == "network" {
					var n hostnetwork.Command
					if hostnetwork.Decode(*c.Network, &n) != nil {
						t.Error("invalid network command")
						return
					}
					key, err := host.Identity(n.MembershipID)
					if err != nil {
						t.Error(err)
						return
					}
					receipt := hostnetwork.Receipt{Identity: key, NetworkID: n.NetworkID}
					if n.Manifest != nil {
						receipt.Generation = n.Manifest.Generation
						receipt.Hash = hostnetwork.Hash(*n.Manifest)
					}
					raw, _ := json.Marshal(receipt)
					v := json.RawMessage(raw)
					report.Network = &v
				} else if c.Kind == "crosshost" {
					var command deployment.CrossHostCommand
					if hostnetwork.Decode(*c.Crosshost, &command) != nil {
						t.Error("invalid host command")
						return
					}
					if c.ParentId == nil || *c.ParentId != command.ParentID || command.Unit.Plan.ServerID != c.ServerId || command.Digest() != c.PlanHash {
						t.Error("host authority was not frozen")
						return
					}
					checkpointHash := ""
					if command.Phase == "checkpoint" {
						var descriptor deployment.RecoveryDescriptor
						if c.RecoveryCheckpoint == nil || json.Unmarshal(*c.RecoveryCheckpoint, &descriptor) != nil || descriptor.Validate() != nil || !command.Unit.Plan.Local.Primary {
							t.Error("checkpoint lost its assigned primary identity")
							return
						}
						envelope, err := f.execution.RecoveryCheckpoint(ctx, identity, descriptor.ParentID, descriptor.CheckpointID)
						if err != nil || len(envelope) != descriptor.Bytes || deployment.RecoveryDigest(envelope) != descriptor.Digest {
							t.Error("checkpoint envelope changed", err)
							return
						}
						if _, err := f.execution.RecoveryCheckpoint(ctx, identities[1-i], descriptor.ParentID, descriptor.CheckpointID); !errors.Is(err, domain.NotFound) {
							t.Error("another peer received the primary's checkpoint", err)
							return
						}
						seen.Store("checkpoint-delivery/"+c.ParentId.String(), true)
						if holdCheckpoint.Load() {
							time.Sleep(100 * time.Millisecond)
							continue
						}
						checkpointHash = descriptor.Digest
					}
					if command.Phase == "checkpoint-closed" && holdClosure.Load() {
						time.Sleep(25 * time.Millisecond)
						continue
					}
					if i == 1 && command.Phase == "pause" && holdRemotePause.Load() {
						time.Sleep(100 * time.Millisecond)
						continue
					}
					if i == 1 && command.Phase == "reserve" && holdRemoteReserve.Load() {
						time.Sleep(100 * time.Millisecond)
						continue
					}
					if i == 1 && command.Phase == "prepare" && failCandidate.Load() {
						report.State = "failed"
					}
					if i == 1 && command.Phase == "resume-target" && holdRemoteResume.Load() {
						time.Sleep(100 * time.Millisecond)
						continue
					}
					seen.Store(command.Phase+"/"+c.ServerId.String(), true)
					seen.Store(command.ParentID.String()+"/"+command.Phase+"/"+c.ServerId.String(), true)
					observation := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}
					if command.Phase == "resolve" {
						observation.Images = map[string]string{}
						for _, app := range command.Unit.Plan.LocalApplications() {
							observation.Images[app.ID.String()] = *app.Source.Image
						}
					}
					if command.Phase == "complete" || command.Phase == "observe" {
						observation.Applied = command.Phase == "complete"
						plan := command.Unit.Plan
						if command.UseTarget && command.Maintenance != nil && command.Maintenance.Target != nil {
							plan = *command.Maintenance.Target
						}
						if command.Phase == "complete" {
							if plan.Local.Primary || len(plan.LocalApplications()) > 0 {
								unitPlans.Store(plan.UnitID, plan)
							} else {
								unitPlans.Delete(plan.UnitID)
							}
						}
						for _, app := range plan.LocalApplications() {
							resource := deployment.Resource{ID: app.ID, Kind: "service", State: "running", Image: plan.Images[app.ID.String()], Logs: "sample from " + c.ServerId.String()}
							for _, id := range plan.InstanceIDs(app.ID) {
								resource.Instances = append(resource.Instances, deployment.InstanceState{InstanceID: id, State: "running"})
							}
							observation.Resources = append(observation.Resources, resource)
						}
					}
					receipt := deployment.CrossHostReceipt{ParentID: command.ParentID, GroupHash: command.GroupHash, UnitID: c.ResourceId, Phase: command.Phase, PlanHash: command.Unit.Plan.Hash, Observation: observation, CheckpointHash: checkpointHash}
					raw, _ := json.Marshal(receipt)
					v := json.RawMessage(raw)
					report.Crosshost = &v
				} else if c.Kind == "upgrade" {
					var command upgrade.Command
					if c.ParentId == nil || c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &command) != nil || command.Plan.Group == nil || command.Digest() != c.PlanHash {
						t.Error("upgrade lost its frozen group authority")
						return
					}
					for _, unit := range command.Plan.Group.ActiveUnits() {
						if _, ok := seen.Load(c.ParentId.String() + "/pause/" + unit.Plan.ServerID.String()); !ok {
							t.Error("upgrade dispatched before every writer stop")
							return
						}
					}
					status := upgrade.Status{Phase: "ready", Step: "verified", Serving: "none", Snapshot: strings.Repeat("c", 64), KeepUntil: time.Now().Add(time.Hour), Note: "Fixture database outcome"}
					if command.Action != "prepare" {
						plan := command.Plan.Target()
						status.Phase, status.Serving, status.WritesPossible = "complete", "new", true
						if command.Action == "return" {
							plan = command.Plan.Source
							status.Phase, status.Serving, status.WritesPossible = "returned", "old", false
						}
						observation := deployment.Observation{ObservedAt: time.Now().UTC(), Applied: true, StateHash: strings.Repeat("d", 64), Resources: []deployment.Resource{}}
						for _, app := range plan.LocalApplications() {
							state := "exited"
							if app.ID == command.Plan.ResourceID {
								state = "running"
							}
							res := deployment.Resource{ID: app.ID, Kind: "service", State: state, Image: plan.Images[app.ID.String()]}
							for _, id := range plan.InstanceIDs(app.ID) {
								res.Instances = append(res.Instances, deployment.InstanceState{InstanceID: id, State: state})
							}
							observation.Resources = append(observation.Resources, res)
						}
						raw, _ := json.Marshal(observation)
						value := json.RawMessage(raw)
						report.Observation = &value
					}
					raw, _ := json.Marshal(status)
					value := json.RawMessage(raw)
					report.Upgrade = &value
				} else if c.Kind == "backup" || c.Kind == "schedule" {
					var command backup.Command
					if c.Backup == nil || json.Unmarshal(*c.Backup, &command) != nil || command.Action != "backup" || c.ParentId == nil || command.Installation.Policy.Plan.Local == nil || !command.Installation.Policy.Plan.Local.Primary {
						t.Error("backup did not retain its coordinated primary authority")
						return
					}
					for _, server := range servers {
						if _, ok := seen.Load("pause/" + server.Server.Id.String()); !ok {
							t.Error("backup dispatched before every writer pause receipt")
							return
						}
					}
					seen.Store("backup/"+c.ServerId.String(), true)
					if command.ScheduledAt != nil {
						p := command.Installation.Policy
						if command.RunID != backup.RunID(p, *command.ScheduledAt) {
							t.Error("scheduled backup changed its occurrence identity")
							return
						}
						status := backup.Status{PolicyID: p.ID, Generation: p.Generation, Enabled: true, Next: *command.ScheduledAt, Runs: []backup.Run{{ID: command.RunID, PolicyID: p.ID, Generation: p.Generation, ScheduledAt: *command.ScheduledAt, State: "succeeded"}}}
						if err := f.execution.ScheduleReport(ctx, identity, 2, status); err != nil {
							t.Error(err)
							return
						}
					}
				} else if c.Kind == "test" {
					report.State = "needs_attention"
					time.Sleep(100 * time.Millisecond)
				} else if c.Kind == "visual" {
					var payload deployment.Payload
					if json.Unmarshal(*c.Visual, &payload) != nil {
						t.Error("invalid initial primary plan")
						return
					}
					observation := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("b", 64)}
					if payload.Action == "resolve" {
						observation.Images = map[string]string{}
						for _, app := range payload.Plan.Specification.Applications {
							observation.Images[app.ID.String()] = *app.Source.Image
						}
					} else {
						observation.Applied = true
						unitPlans.Store(payload.Plan.UnitID, payload.Plan)
						for _, app := range payload.Plan.Specification.Applications {
							resource := deployment.Resource{ID: app.ID, Kind: "service", State: "running", Image: payload.Plan.Images[app.ID.String()]}
							for _, id := range payload.Plan.InstanceIDs(app.ID) {
								resource.Instances = append(resource.Instances, deployment.InstanceState{InstanceID: id, State: "running"})
							}
							observation.Resources = append(observation.Resources, resource)
						}
					}
					raw, _ := json.Marshal(observation)
					value := json.RawMessage(raw)
					report.Observation = &value
				} else {
					t.Error("unexpected host operation", c.Kind)
					return
				}
				if err := f.execution.Report(ctx, identity, report); err != nil {
					if ctx.Err() == nil && !(i == 0 && revokePrimary.Load() && errors.Is(err, domain.Unauthenticated)) {
						t.Error("report", c.Kind, err)
					}
					return
				}
			}
		})
	}
	defer func() { cancel(); wg.Wait() }()
	var network struct {
		ID         uuid.UUID `json:"id"`
		Generation int64     `json:"generation"`
		State      string    `json:"state"`
	}
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", Members: members}, 200, &network)
	wait := func(test func() bool) {
		t.Helper()
		deadline := time.Now().Add(65 * time.Second)
		for time.Now().Before(deadline) {
			if test() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		var diagnostic []byte
		_ = f.st.Pool.QueryRow(context.Background(), `SELECT jsonb_agg(jsonb_build_object('phase',h.phase,'state',o.state,'note',o.note,'errors',j.errors)) FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id LEFT JOIN river_job j ON j.id=o.job_id WHERE h.workspace_id=$1`, ws).Scan(&diagnostic)
		var networkDiagnostic []byte
		_ = f.st.Pool.QueryRow(context.Background(), `SELECT jsonb_build_object('members',(SELECT jsonb_agg(jsonb_build_object('server',m.server_id,'enabled',m.enabled,'generation',m.generation,'ack',m.acknowledged_generation,'installed',m.installed_manifest,'controller',s.controller_generation,'approved',m.approved_controller,'revoked',s.revoked,'seen',s.last_seen)) FROM network_members m JOIN servers s ON s.id=m.server_id WHERE m.workspace_id=$1),'operations',(SELECT jsonb_agg(jsonb_build_object('server',o.server_id,'state',o.state,'note',o.note,'command',o.command->'network','errors',j.errors)) FROM execution_operations o LEFT JOIN river_job j ON j.id=o.job_id WHERE o.workspace_id=$1 AND o.kind='network' AND o.state NOT IN ('succeeded','failed','cancelled')))`, ws).Scan(&networkDiagnostic)
		t.Fatalf("coordinated transition timed out: %s; network: %s", diagnostic, networkDiagnostic)
	}
	wait(func() bool { b.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
	app := uuid.New()
	image := execution.TestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Replicas: 2, Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "placement.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}, Placement: &domain.Placement{NetworkID: network.ID, IngressServerID: servers[0].Server.Id, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 500, MemoryMiB: 128, Targets: []domain.PlacementTarget{{ServerID: servers[1].Server.Id, Count: 2}}}}}}
	volume := uuid.New()
	spec.Volumes = []domain.Volume{{ID: volume, Name: "Files"}}
	var draft api.Revision
	initial := spec
	initial.Placement = nil
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": initial}, 200, &draft)
	var primary deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: servers[0].Server.Id, AllowHostChanges: true}, 201, &primary)
	wait(func() bool {
		b.call("GET", path+"/deployment-plans/"+primary.ID.String(), nil, 200, &primary)
		return primary.Plan != nil
	})
	var firstOperation api.Operation
	b.call("POST", path+"/deployment-plans/"+primary.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &firstOperation)
	wait(func() bool {
		var current deployment.Observation
		b.call("GET", path+"/runtime", nil, 200, &current)
		return current.Applied
	})
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)

	var status deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: servers[0].Server.Id, AllowHostChanges: true}, 201, &status)
	wait(func() bool {
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status.Plan != nil
	})
	if status.Plan.Validate(true) != nil || status.Plan.Distributed == nil || len(status.Plan.Distributed.Units) != 2 {
		t.Fatal("incomplete reviewed group")
	}
	apply := path + "/deployment-plans/" + status.ID.String() + "/apply"
	// Cancel after the checkpoint reaches the host, before its report releases
	// reservation. Management must send an explicit no-effect closure afterward.
	holdCheckpoint.Store(true)
	var cancelledCheckpoint api.Operation
	b.call("POST", apply, api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &cancelledCheckpoint)
	wait(func() bool { _, ok := seen.Load("checkpoint-delivery/" + cancelledCheckpoint.Id.String()); return ok })
	b.call("POST", base+"/operations/"+cancelledCheckpoint.Id.String()+"/cancel", nil, 204, nil)
	holdCheckpoint.Store(false)
	wait(func() bool {
		var closed bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations p JOIN execution_operations c ON c.parent_id=p.id WHERE p.id=$1 AND p.state='cancelled' AND c.command->'crosshost'->>'phase'='checkpoint-closed' AND c.state='succeeded')`, cancelledCheckpoint.Id).Scan(&closed); err != nil {
			t.Fatal(err)
		}
		return closed
	})
	holdClosure.Store(true)
	var closureRaw []byte
	if err := f.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='checkpoint-closed'`, cancelledCheckpoint.Id).Scan(&closureRaw); err != nil {
		t.Fatal(err)
	}
	var closureFrame api.ExecutionCommand
	if json.Unmarshal(closureRaw, &closureFrame) != nil {
		t.Fatal("invalid closure frame")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET cancel_requested=false WHERE id=$1`, cancelledCheckpoint.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='running',report_sequence=0 WHERE id=$1`, closureFrame.Id); err != nil {
		t.Fatal(err)
	}
	acceptedClosure := api.AgentReport{Id: closureFrame.Id, Generation: closureFrame.Generation, PlanHash: closureFrame.PlanHash, Sequence: 1, State: "running", Steps: []api.ExecutionStep{}}
	if err := f.execution.Report(ctx, identities[0], acceptedClosure); err != nil {
		t.Fatal(err)
	}
	var cancellationRestored bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT cancel_requested FROM execution_operations WHERE id=$1`, cancelledCheckpoint.Id).Scan(&cancellationRestored); err != nil || !cancellationRestored {
		t.Fatal("accepted closure lost authoritative cancellation intent", err)
	}
	var closureBody deployment.CrossHostCommand
	if json.Unmarshal(*closureFrame.Crosshost, &closureBody) != nil {
		t.Fatal("invalid closure payload")
	}
	closureReceipt := deployment.CrossHostReceipt{ParentID: closureBody.ParentID, GroupHash: closureBody.GroupHash, UnitID: closureFrame.ResourceId, PlanHash: closureBody.Unit.Plan.Hash, Phase: closureBody.Phase, Observation: deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}}}
	rawClosureReceipt, _ := json.Marshal(closureReceipt)
	closureValue := json.RawMessage(rawClosureReceipt)
	acceptedClosure.State, acceptedClosure.Sequence, acceptedClosure.Crosshost = "succeeded", 2, &closureValue
	if err := f.execution.Report(ctx, identities[0], acceptedClosure); err != nil {
		t.Fatal(err)
	}
	holdClosure.Store(false)
	var reservedAfterCancel int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase' IN ('reserve','prepare','publish')`, cancelledCheckpoint.Id).Scan(&reservedAfterCancel); err != nil || reservedAfterCancel != 0 {
		t.Fatal("checkpoint cancellation dispatched workload effects", err)
	}
	var activeCancelledSources int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM network_endpoint_sources s JOIN network_plan_sources p ON p.endpoint_id=s.endpoint_id AND p.server_id=s.server_id AND p.unit_id=s.unit_id WHERE p.plan_id=$1 AND s.active`, status.Plan.ID).Scan(&activeCancelledSources); err != nil || activeCancelledSources != 0 {
		t.Fatal("checkpoint cancellation left candidate-only network grants active", err)
	}
	wait(func() bool { b.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: servers[0].Server.Id, AllowHostChanges: true}, 201, &status)
	wait(func() bool {
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status.Plan != nil
	})
	apply = path + "/deployment-plans/" + status.ID.String() + "/apply"
	key := uuid.New()
	var operation, again api.Operation
	holdCheckpoint.Store(true)
	b.call("POST", apply, api.ApplyPlan{IdempotencyKey: key}, 201, &operation)
	b.call("POST", apply, api.ApplyPlan{IdempotencyKey: key}, 201, &again)
	if again.Id != operation.Id {
		t.Fatal("duplicate group admission")
	}
	wait(func() bool {
		_, delivered := seen.Load("checkpoint-delivery/" + operation.Id.String())
		return delivered
	})
	var earlyReserve bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='reserve')`, operation.Id).Scan(&earlyReserve); err != nil || earlyReserve {
		t.Fatal("host reservation preceded the primary recovery checkpoint acknowledgement", err)
	}
	holdCheckpoint.Store(false)
	wait(func() bool {
		var ops []api.Operation
		b.call("GET", base+"/operations", nil, 200, &ops)
		for _, op := range ops {
			if op.Id == operation.Id {
				if op.State == "failed" || op.State == "needs_attention" {
					t.Fatalf("parent: %+v", op)
				}
				return op.State == "succeeded"
			}
		}
		return false
	})
	for _, phase := range []string{"reserve", "prepare", "retire", "complete"} {
		for _, server := range servers {
			if _, ok := seen.Load(phase + "/" + server.Server.Id.String()); !ok {
				t.Fatal("missing phase acknowledgement", phase)
			}
		}
	}
	var runtime deployment.Observation
	b.call("GET", path+"/runtime", nil, 200, &runtime)
	if !runtime.Applied || len(runtime.Resources) != 1 || len(runtime.Resources[0].Instances) != 2 {
		t.Fatal("aggregate runtime lost a placement")
	}
	var primaryReceipts int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM cross_host_runtime WHERE workspace_id=$1 AND server_id=$2 AND jsonb_array_length(inventory->'observation'->'resources')=0`, ws, servers[0].Server.Id).Scan(&primaryReceipts); err != nil || primaryReceipts != 1 {
		t.Fatal("empty ingress primary lost its authenticated maintenance baseline", err)
	}
	// A replacement changes both placement and the HTTP claim, then inspection
	// must refresh the applied group rather than the latest candidate or one host.
	oldDomain := spec.Routes[0].Domain
	spec.Routes[0].Domain = "replacement.example.test"
	spec.Placement.Applications[0].Targets = []domain.PlacementTarget{{ServerID: servers[0].Server.Id, Count: 1}, {ServerID: servers[1].Server.Id, Count: 1}}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: servers[0].Server.Id, AllowHostChanges: true}, 201, &status)
	wait(func() bool {
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status.Plan != nil
	})
	b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operation)
	wait(func() bool {
		b.call("GET", path+"/runtime", nil, 200, &runtime)
		return runtime.StateHash == status.Plan.Hash
	})
	var oldClaims int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM route_claims WHERE workspace_id=$1 AND claim=$2`, ws, "http:"+oldDomain).Scan(&oldClaims); err != nil || oldClaims != 0 {
		t.Fatal("successful group replacement kept its removed route claim", err)
	}
	before := runtime.ObservedAt
	b.call("POST", path+"/runtime/refresh", nil, 201, &operation)
	wait(func() bool {
		b.call("GET", path+"/runtime", nil, 200, &runtime)
		return runtime.ObservedAt.After(before) && len(runtime.Hosts) == 2 && runtime.Hosts[0].State == "snapshot" && runtime.Hosts[1].State == "snapshot"
	})
	if len(runtime.Resources) != 1 || len(runtime.Resources[0].Instances) != 2 || !strings.HasPrefix(runtime.Resources[0].Note, "2 of 2") {
		t.Fatal("inspection collapsed the applied placement group")
	}
	// Receipt timestamps use the database clock; the requested export window
	// must reach them before immediate post-inspection samples are eligible.
	wait(func() bool {
		var logs api.LogExport
		b.call("GET", path+"/logs/export?hours=1", nil, 200, &logs)
		logged := map[uuid.UUID]bool{}
		for _, entry := range logs.Entries {
			if entry.ServerId != nil && entry.UnitId != nil && strings.Contains(entry.Content, entry.ServerId.String()) {
				logged[*entry.ServerId] = true
			}
		}
		return len(logged) == 2
	})
	var destination backup.Destination
	b.call("POST", base+"/backup-destinations", map[string]any{"name": "Fixture", "kind": "filesystem", "root": "/srv/fixture-backups", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &destination)
	password := "fixture-repository-password"
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Coordinated files", ResourceId: volume, DestinationId: destination.ID, Method: "quiesced-files", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	if !policy.Coordinated || !policy.RequiresQuiescence() || policy.Plan.Local == nil || !policy.Plan.Local.Primary {
		t.Fatal("valid quiescing policy did not select coordinated primary maintenance")
	}
	var unrelated api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Unrelated"}, 201, &unrelated)
	standalone := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO supported_upgrades(id,author_id,workspace_id,environment_id,server_id,plan,status) VALUES($1,$2,$3,$4,$5,'{}','{"phase":"ready"}')`, standalone, f.owner, ws, unrelated.Id, servers[1].Server.Id); err != nil {
		t.Fatal(err)
	}
	b.call("POST", path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "backup", RequestKey: uuid.New(), Snapshot: ""}, 409, nil)
	var blockedReservations int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM cross_host_reservations WHERE workspace_id=$1`, ws).Scan(&blockedReservations); err != nil || blockedReservations != 0 {
		t.Fatal("a secondary upgrade was captured by a new reservation", err)
	}
	if _, err := f.st.Pool.Exec(ctx, `DELETE FROM supported_upgrades WHERE workspace_id=$1 AND id=$2`, ws, standalone); err != nil {
		t.Fatal(err)
	}
	backupKey := uuid.New()
	var maintenance, repeated api.Operation
	holdRemoteReserve.Store(true)
	holdRemotePause.Store(true)
	b.call("POST", path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "backup", RequestKey: backupKey, Snapshot: ""}, 202, &maintenance)
	b.call("POST", path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "backup", RequestKey: backupKey, Snapshot: ""}, 202, &repeated)
	if maintenance.Kind != "crosshost" || maintenance.Id != repeated.Id {
		t.Fatal("quiescing backup did not create one durable parent")
	}
	remoteUnit := status.Plan.Distributed.Units[0].Plan.UnitID
	for _, unit := range status.Plan.Distributed.Units {
		if unit.Plan.ServerID == servers[1].Server.Id {
			remoteUnit = unit.Plan.UnitID
		}
	}
	spoofKey := uuid.NewSHA1(maintenance.Id, []byte("crosshost:maintenance-pause:"+remoteUnit.String()))
	spoofID := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `UPDATE environments SET execution_generation=1 WHERE workspace_id=$1 AND id=$2`, ws, unrelated.Id); err != nil {
		t.Fatal(err)
	}
	// A caller-controlled request key must not identify an internal child.
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,1,$5,'{}','pipeline','succeeded')`, spoofID, ws, servers[1].Server.Id, unrelated.Id, spoofKey); err != nil {
		t.Fatal(err)
	}
	holdRemoteReserve.Store(false)
	wait(func() bool {
		var blocked bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-pause' AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$2 AND c.command->'crosshost'->>'phase'='pause' AND c.state='succeeded') AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$3 AND c.command->'crosshost'->>'phase'='pause' AND c.state='dispatched') FROM cross_host_operations h WHERE h.id=$1`, maintenance.Id, servers[0].Server.Id, servers[1].Server.Id).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	time.Sleep(3 * time.Second)
	var prematureEffects int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND kind IN ('backup','schedule')`, maintenance.Id).Scan(&prematureEffects); err != nil || prematureEffects != 0 {
		t.Fatal("an unconfirmed remote writer permitted a primary backup", err)
	}
	holdRemotePause.Store(false)
	wait(func() bool {
		var ops []api.Operation
		b.call("GET", base+"/operations", nil, 200, &ops)
		for _, op := range ops {
			if op.Id == maintenance.Id {
				if op.State == "failed" || op.State == "needs_attention" {
					t.Fatalf("maintenance parent: %+v", op)
				}
				return op.State == "succeeded"
			}
		}
		return false
	})
	if _, ok := seen.Load("backup/" + servers[0].Server.Id.String()); !ok {
		t.Fatal("coordinated backup omitted the primary effect")
	}
	var reservations int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM cross_host_reservations WHERE workspace_id=$1", ws).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatal("completed group retained server reservations", err)
	}
	// Concurrent management sweeps coalesce an outage to one frozen occurrence.
	// A later agent report still carries its installation cursor and cannot rewind it.
	now := time.Now().UTC()
	due := policy.Schedule.Next(now.Add(-72 * time.Hour))
	wantAt, wantNext, err := policy.Schedule.Due(due, now)
	if err != nil {
		t.Fatal(err)
	}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/schedule-reports", identities[0].ID.String()+"."+identities[0].Token, map[string]any{"sequence": 1, "status": map[string]any{"policyId": policy.ID, "generation": policy.Generation, "enabled": true, "next": "0000-01-01T00:00:00Z", "runs": []any{}}}, 400, nil)
	// Installed policies awaiting host transfer can fill a batch indefinitely.
	// Their blocked admission must not prevent another workspace's due snapshot.
	var blockedWorkspace api.Workspace
	b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Pending transfer"}, 201, &blockedWorkspace)
	blockedEnv := envForExecution(t, f, b, blockedWorkspace.Id)
	blockedServer, _, _ := enrollment(t, f, b, blockedWorkspace.Id, "deployment")
	if _, err := f.st.Pool.Exec(ctx, `UPDATE servers SET awaiting_transfer=true,controller_generation=1 WHERE workspace_id=$1 AND id=$2`, blockedWorkspace.Id, blockedServer.Server.Id); err != nil {
		t.Fatal(err)
	}
	configuration, _ := json.Marshal(policy)
	var blockedPolicy backup.Policy
	if err := json.Unmarshal(configuration, &blockedPolicy); err != nil {
		t.Fatal(err)
	}
	blockedPolicy.WorkspaceID, blockedPolicy.EnvironmentID, blockedPolicy.ServerID = blockedWorkspace.Id, blockedEnv, blockedServer.Server.Id
	priorServer := blockedPolicy.Plan.ServerID
	blockedPolicy.Plan.EnvironmentID, blockedPolicy.Plan.ServerID = blockedEnv, blockedServer.Server.Id
	blockedPolicy.Plan.Specification.Placement.IngressServerID = blockedServer.Server.Id
	for i := range blockedPolicy.Plan.Specification.Placement.Applications {
		for j := range blockedPolicy.Plan.Specification.Placement.Applications[i].Targets {
			target := &blockedPolicy.Plan.Specification.Placement.Applications[i].Targets[j]
			if target.ServerID == priorServer {
				target.ServerID = blockedServer.Server.Id
			}
		}
	}
	for key, endpoints := range blockedPolicy.Plan.Local.Backends {
		for i := range endpoints {
			if endpoints[i].ServerID == priorServer {
				endpoints[i].ServerID = blockedServer.Server.Id
			}
		}
		blockedPolicy.Plan.Local.Backends[key] = endpoints
	}
	blockedPolicy.Plan.Local.Network.ServerID = blockedServer.Server.Id
	blockedPolicy.Plan = blockedPolicy.Plan.WithHash()
	if err := blockedPolicy.Validate(); err != nil {
		t.Fatal("invalid pending-transfer policy fixture", err)
	}
	configuration, _ = json.Marshal(blockedPolicy)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access,status,status_sequence)
	 SELECT id,$1,$2,$3,jsonb_set($4::jsonb,'{id}',to_jsonb(id::text)),$5,
	 jsonb_build_object('policyId',id,'generation',1,'enabled',true,'next',$6::text,'runs','[]'::jsonb),1
	 FROM (SELECT lpad(to_hex(n),32,'0')::uuid id FROM generate_series(1,100) n) seeds`, blockedWorkspace.Id, blockedEnv, blockedServer.Server.Id, configuration, []byte("unused-pending-transfer-access"), due.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// A malformed timestamp in an imported status is isolated too; no global
	// PostgreSQL cast may abort all tenants' scheduling.
	var invalidDestination backup.Destination
	b.call("POST", base+"/backup-destinations", map[string]any{"name": "Imported destination", "kind": "filesystem", "root": "/srv/fixture-imported", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &invalidDestination)
	var poisoned backup.Policy
	b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Imported timestamp", ResourceId: volume, DestinationId: invalidDestination.ID, Method: "quiesced-files", Schedule: api.DailySchedule{Hour: (now.Hour() + 22) % 24, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &poisoned)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_policies SET status=jsonb_build_object('policyId',id,'generation',1,'enabled',true,'next','0000-01-01T00:00:00Z','runs','[]'::jsonb) WHERE workspace_id=$1 AND id=$2`, ws, poisoned.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.ScheduleReport(ctx, identities[0], 1, backup.Status{PolicyID: policy.ID, Generation: policy.Generation, Enabled: true, Next: due, Runs: []backup.Run{}}); err != nil {
		t.Fatal(err)
	}
	firstSweep, err := f.execution.Queue.Insert(ctx, coordinatedScheduleSweep{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		var state string
		if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id=$1`, firstSweep.Job.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state == "completed"
	})
	// A valid timestamp which is not a cadence occurrence must also be scoped
	// to that policy, rather than become a global worker failure.
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_policies SET status=jsonb_set(status,'{next}',to_jsonb($3::text)) WHERE workspace_id=$1 AND id=$2`, ws, poisoned.ID, now.Add(-time.Minute).Truncate(time.Second).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	var sweepIDs []int64
	for range 4 {
		job, err := f.execution.Queue.Insert(ctx, coordinatedScheduleSweep{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		sweepIDs = append(sweepIDs, job.Job.ID)
	}
	wait(func() bool {
		var completed int
		if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE id=ANY($1::bigint[]) AND state='completed'`, sweepIDs).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		return completed == len(sweepIDs)
	})
	wait(func() bool {
		var ready bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND state='succeeded' AND command->'crosshost'->'maintenance'->'effect'->'backup'->'installation'->'policy'->>'id'=$2 AND command->'crosshost'->'maintenance'->'effect'->'backup'->>'scheduledAt' IS NOT NULL)`, ws, policy.ID.String()).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		return ready
	})
	var scheduledCount int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND command->'crosshost'->'maintenance'->'effect'->'backup'->'installation'->'policy'->>'id'=$2 AND command->'crosshost'->'maintenance'->'effect'->'backup'->>'scheduledAt' IS NOT NULL`, ws, policy.ID.String()).Scan(&scheduledCount); err != nil || scheduledCount != 1 {
		t.Fatal("scheduled occurrence created more than one parent", err)
	}
	var scheduled backup.Status
	b.call("GET", path+"/backup-policies/"+policy.ID.String()+"/status", nil, 200, &scheduled)
	if !scheduled.Next.Equal(wantNext) || len(scheduled.Runs) != 1 || scheduled.Runs[0].ID != backup.RunID(policy, wantAt) || !scheduled.Runs[0].ScheduledAt.Equal(wantAt) {
		t.Fatalf("coordinated cursor or occurrence was lost: %+v", scheduled)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_destinations SET configuration='{"root":42}' WHERE workspace_id=$1 AND id=$2`, ws, invalidDestination.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_policies SET status=jsonb_set(status,'{next}',to_jsonb($3::text)) WHERE workspace_id=$1 AND id=$2`, ws, poisoned.ID, poisoned.Schedule.Next(now.Add(-48*time.Hour)).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	malformedSweep, err := f.execution.Queue.Insert(ctx, coordinatedScheduleSweep{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		var state string
		if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id=$1`, malformedSweep.Job.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state == "completed"
	})
	validDestination, _ := json.Marshal(invalidDestination)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_destinations SET configuration=$3 WHERE workspace_id=$1 AND id=$2`, ws, invalidDestination.ID, validDestination); err != nil {
		t.Fatal(err)
	}
	var sourcePlanID uuid.UUID
	var sourcePlanRaw []byte
	if err := f.st.Pool.QueryRow(ctx, `SELECT r.plan_id,p.plan FROM visual_runtime r JOIN visual_plans p ON p.id=r.plan_id WHERE r.workspace_id=$1 AND r.environment_id=$2`, ws, env.Id).Scan(&sourcePlanID, &sourcePlanRaw); err != nil {
		t.Fatal(err)
	}
	var malformedPlan deployment.Plan
	if err := json.Unmarshal(sourcePlanRaw, &malformedPlan); err != nil {
		t.Fatal(err)
	}
	malformedPlan.Distributed.Units[0].Plan.Local = nil
	malformedPlan = malformedPlan.WithHash()
	malformedStructure, _ := json.Marshal(malformedPlan)
	for _, invalid := range []json.RawMessage{json.RawMessage(`{"hash":42}`), json.RawMessage(malformedStructure)} {
		invalidPlanID := uuid.New()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) SELECT $1,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate FROM visual_requests WHERE workspace_id=$2 AND id=$3`, invalidPlanID, ws, sourcePlanID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)`, invalidPlanID, ws, env.Id, []byte(invalid)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(ctx, `UPDATE visual_runtime SET plan_id=$3 WHERE workspace_id=$1 AND environment_id=$2`, ws, env.Id, invalidPlanID); err != nil {
			t.Fatal(err)
		}
		job, err := f.execution.Queue.Insert(ctx, coordinatedScheduleSweep{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		wait(func() bool {
			var state string
			if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id=$1`, job.Job.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			return state == "completed"
		})
		if _, err := f.st.Pool.Exec(ctx, `UPDATE visual_runtime SET plan_id=$3 WHERE workspace_id=$1 AND environment_id=$2`, ws, env.Id, sourcePlanID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.Pool.Exec(ctx, `DELETE FROM backup_policies WHERE workspace_id=$1 OR (workspace_id=$2 AND id=$3)`, blockedWorkspace.Id, ws, poisoned.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `DELETE FROM backup_destinations WHERE workspace_id=$1 AND id=$2`, ws, invalidDestination.ID); err != nil {
		t.Fatal(err)
	}
	var databaseEnv api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Upgrade"}, 201, &databaseEnv)
	databasePath := base + "/projects/" + project.Id.String() + "/environments/" + databaseEnv.Id.String()
	var databaseSecret api.SecretVersion
	b.call("POST", databasePath+"/secrets", map[string]string{"name": "Database", "value": "group-upgrade-database-canary"}, 201, &databaseSecret)
	web, pg, data := uuid.New(), uuid.New(), uuid.New()
	pgImage := domain.Postgres17Image
	stack := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{
		{ID: web, Name: "Web", Replicas: 2, Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}},
		{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgImage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: databaseSecret.Id}, Mounts: []domain.VolumeMount{{VolumeID: data, Target: domain.PostgresMount(pgImage)}}},
	}, Volumes: []domain.Volume{{ID: data, Name: "Data"}}, Bindings: []domain.Binding{{ID: uuid.New(), SourceID: pg, TargetID: web, Variable: "DATABASE_URL"}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: web, Protocol: "http", Domain: "upgrade.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var databaseDraft api.Revision
	b.call("PUT", databasePath+"/draft", map[string]any{"baseRevisionId": databaseEnv.DraftRevisionId, "specification": stack}, 200, &databaseDraft)
	deploy := func(revision uuid.UUID) deployment.Plan {
		t.Helper()
		var plan deployment.Status
		b.call("POST", databasePath+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: revision, ServerId: servers[0].Server.Id, AllowHostChanges: true}, 201, &plan)
		wait(func() bool {
			b.call("GET", databasePath+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
			return plan.Plan != nil
		})
		var op api.Operation
		b.call("POST", databasePath+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
		wait(func() bool {
			var state string
			if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, op.Id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state == "failed" || state == "needs_attention" {
				t.Fatalf("upgrade fixture deployment failed: %s", state)
			}
			return state == "succeeded"
		})
		return *plan.Plan
	}
	deploy(databaseDraft.Id)
	stack.Placement = &domain.Placement{NetworkID: network.ID, IngressServerID: servers[0].Server.Id, Applications: []domain.ApplicationPlacement{{ApplicationID: web, CPUMillis: 500, MemoryMiB: 128, Targets: []domain.PlacementTarget{{ServerID: servers[0].Server.Id, Count: 1}, {ServerID: servers[1].Server.Id, Count: 1}}}}}
	b.call("PUT", databasePath+"/draft", map[string]any{"baseRevisionId": databaseDraft.Id, "specification": stack}, 200, &databaseDraft)
	source := deploy(databaseDraft.Id)
	var recovery backup.Policy
	b.call("POST", databasePath+"/backup-policies", api.BackupPolicyRequest{Name: "Recovery", ResourceId: pg, DestinationId: destination.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &recovery)
	var view upgrade.View
	b.call("POST", databasePath+"/upgrades", api.UpgradeRequest{PolicyId: recovery.ID, ResourceId: pg, BaseRevisionId: databaseDraft.Id, RetainDays: 7}, 201, &view)
	actions := databasePath + "/upgrades/" + view.Plan.ID.String() + "/actions"
	prepare := api.UpgradeActionRequest{Action: "prepare", ExpectedPhase: "planned", RequestKey: uuid.New()}
	var parent, same api.Operation
	b.call("POST", actions, prepare, 202, &parent)
	b.call("POST", actions, prepare, 202, &same)
	if parent.Kind != "crosshost" || same.Id != parent.Id {
		t.Fatal("upgrade prepare did not create one durable parent")
	}
	wait(func() bool {
		var ready bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-owner' AND j.state='completed' FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id JOIN river_job j ON j.id=o.job_id WHERE h.id=$1`, parent.Id).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		return ready
	})
	var ownerReservations int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM cross_host_reservations WHERE parent_id=$1`, parent.Id).Scan(&ownerReservations); err != nil || ownerReservations != 2 {
		t.Fatal("owner review released writer reservations", err)
	}
	holdRemoteResume.Store(true)
	cutover := api.UpgradeActionRequest{Action: "cutover", ExpectedPhase: "ready", RequestKey: uuid.New()}
	var cutoverParent, duplicate api.Operation
	b.call("POST", actions, cutover, 202, &cutoverParent)
	b.call("POST", actions, cutover, 202, &duplicate)
	if cutoverParent.Id != parent.Id || duplicate.Id != parent.Id {
		t.Fatal("owner action replaced the upgrade parent")
	}
	wait(func() bool {
		var blocked bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-resume-target' AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$2 AND c.command->'crosshost'->>'phase'='resume-target' AND c.state='dispatched') FROM cross_host_operations h WHERE h.id=$1`, parent.Id, servers[1].Server.Id).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	var applied, current uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, `SELECT e.applied_revision_id,r.plan_id FROM environments e JOIN visual_runtime r ON r.workspace_id=e.workspace_id AND r.environment_id=e.id WHERE e.workspace_id=$1 AND e.id=$2`, ws, databaseEnv.Id).Scan(&applied, &current); err != nil || applied != source.RevisionID || current != source.ID {
		t.Fatal("a primary-only result published a globally applied target", err)
	}
	holdRemoteResume.Store(false)
	wait(func() bool {
		var state string
		if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, parent.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "failed" || state == "needs_attention" {
			t.Fatalf("upgrade completion failed: %s", state)
		}
		return state == "succeeded"
	})
	if err := f.st.Pool.QueryRow(ctx, `SELECT e.applied_revision_id,r.plan_id FROM environments e JOIN visual_runtime r ON r.workspace_id=e.workspace_id AND r.environment_id=e.id WHERE e.workspace_id=$1 AND e.id=$2`, ws, databaseEnv.Id).Scan(&applied, &current); err != nil || applied != view.Plan.TargetRevisionID || current != view.Plan.TargetPlanID {
		t.Fatal("complete group receipts did not publish the target", err)
	}
	var enabled bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT (configuration->>'enabled')::boolean FROM backup_policies WHERE id=$1`, recovery.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("upgraded policy did not require renewed approval", err)
	}
	var beforeEndpoints, beforeSources int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM network_endpoints WHERE workspace_id=$1 AND environment_id=$2 AND NOT retired`, ws, databaseEnv.Id).Scan(&beforeEndpoints); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM network_endpoint_sources s JOIN network_endpoints e ON e.id=s.endpoint_id WHERE e.workspace_id=$1 AND e.environment_id=$2 AND s.active`, ws, databaseEnv.Id).Scan(&beforeSources); err != nil {
		t.Fatal(err)
	}
	if beforeEndpoints == 0 || beforeSources == 0 {
		t.Fatal("upgrade fixture has no private grants")
	}
	targetSpec := view.Plan.TargetGroup().Specification
	changed := "candidate"
	targetSpec.Applications[0].Variables = []domain.Variable{{Name: "CANDIDATE", Value: &changed}}
	var failedDraft api.Revision
	b.call("PUT", databasePath+"/draft", map[string]any{"baseRevisionId": view.Plan.TargetRevisionID, "specification": targetSpec}, 200, &failedDraft)
	var replacement deployment.Status
	b.call("POST", databasePath+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: failedDraft.Id, ServerId: servers[0].Server.Id, AllowHostChanges: true}, 201, &replacement)
	wait(func() bool {
		b.call("GET", databasePath+"/deployment-plans/"+replacement.ID.String(), nil, 200, &replacement)
		return replacement.Plan != nil
	})
	failCandidate.Store(true)
	var failedOperation api.Operation
	b.call("POST", databasePath+"/deployment-plans/"+replacement.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &failedOperation)
	wait(func() bool {
		var state string
		if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, failedOperation.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "needs_attention" {
			t.Fatal("failed candidate did not complete owned abort")
		}
		return state == "failed"
	})
	var afterEndpoints, afterSources int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM network_endpoints WHERE workspace_id=$1 AND environment_id=$2 AND NOT retired`, ws, databaseEnv.Id).Scan(&afterEndpoints); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM network_endpoint_sources s JOIN network_endpoints e ON e.id=s.endpoint_id WHERE e.workspace_id=$1 AND e.environment_id=$2 AND s.active`, ws, databaseEnv.Id).Scan(&afterSources); err != nil {
		t.Fatal(err)
	}
	if afterEndpoints != beforeEndpoints || afterSources != beforeSources {
		t.Fatal("abort after major upgrade removed serving private grants")
	}
	// A restored-volume draft is a capability for one exact source and revision;
	// it must enter writer coordination rather than ordinary pool publication.
	var restoreBase api.Revision
	b.call("PUT", databasePath+"/draft", map[string]any{"baseRevisionId": failedDraft.Id, "specification": view.Plan.TargetGroup().Specification}, 200, &restoreBase)
	b.call("PUT", databasePath+"/backup-policies/"+recovery.ID.String(), api.BackupPolicyRequest{BaseGeneration: recovery.Generation, Name: "Recovery", ResourceId: pg, DestinationId: destination.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 200, &recovery)
	restoredAt := time.Now().UTC()
	manifest := backup.Manifest{Recipe: backup.Recipe, PolicyID: recovery.ID, RunID: uuid.New(), EnvironmentID: databaseEnv.Id, ResourceID: pg, Method: "postgres-logical", Image: domain.PostgresImage, Database: "app", Username: "app", CreatedAt: restoredAt, Roles: []backup.Role{}, Extensions: []string{}, SHA256: strings.Repeat("f", 64), Bytes: 100}
	restored := backup.Run{ID: uuid.New(), PolicyID: recovery.ID, Generation: recovery.Generation, ScheduledAt: restoredAt, StartedAt: restoredAt, FinishedAt: &restoredAt, State: "succeeded", Snapshot: strings.Repeat("e", 64), Verified: true, TargetID: uuid.New(), Manifest: &manifest, ManifestHash: backup.Hash(manifest)}
	historicalManifest := manifest
	historicalManifest.Image = domain.Postgres17Image
	historical := restored
	historical.ID, historical.TargetID = uuid.New(), uuid.New()
	historical.Manifest, historical.ManifestHash = &historicalManifest, backup.Hash(historicalManifest)
	if err := f.execution.ScheduleReport(ctx, identities[0], 1, backup.Status{PolicyID: recovery.ID, Generation: recovery.Generation, Runs: []backup.Run{restored, historical}}); err != nil {
		t.Fatal(err)
	}
	b.call("POST", databasePath+"/backup-policies/"+recovery.ID.String()+"/cutover", api.BackupCutoverRequest{BaseRevisionId: restoreBase.Id, RunId: historical.ID}, 400, nil)
	var restoreDraft api.Revision
	b.call("POST", databasePath+"/backup-policies/"+recovery.ID.String()+"/cutover", api.BackupCutoverRequest{BaseRevisionId: restoreBase.Id, RunId: restored.ID}, 200, &restoreDraft)
	var restoreReview deployment.Status
	b.call("POST", databasePath+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: restoreDraft.Id, ServerId: servers[0].Server.Id, AllowHostChanges: true}, 201, &restoreReview)
	wait(func() bool {
		b.call("GET", databasePath+"/deployment-plans/"+restoreReview.ID.String(), nil, 200, &restoreReview)
		return restoreReview.Plan != nil
	})
	if restoreReview.Plan.Restore == nil || restoreReview.Plan.Restore.SourcePlanID != view.Plan.TargetPlanID {
		t.Fatal("restored-volume review lost its exact source")
	}
	var restoreOperation api.Operation
	holdRemotePause.Store(true)
	holdRemoteResume.Store(true)
	b.call("POST", databasePath+"/deployment-plans/"+restoreReview.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &restoreOperation)
	wait(func() bool {
		var paused bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-pause' AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$2 AND c.command->'crosshost'->>'phase'='pause' AND c.state='succeeded') AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$3 AND c.command->'crosshost'->>'phase'='pause' AND c.state='dispatched') FROM cross_host_operations h WHERE h.id=$1`, restoreOperation.Id, servers[0].Server.Id, servers[1].Server.Id).Scan(&paused); err != nil {
			t.Fatal(err)
		}
		return paused
	})
	var prematureRestore bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='restore')`, restoreOperation.Id).Scan(&prematureRestore); err != nil || prematureRestore {
		t.Fatal("restored data activated with an unconfirmed remote writer", err)
	}
	holdRemotePause.Store(false)
	wait(func() bool {
		var blocked bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-resume-target' AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$2 AND c.command->'crosshost'->>'phase'='resume-target' AND c.state='dispatched') FROM cross_host_operations h WHERE h.id=$1`, restoreOperation.Id, servers[1].Server.Id).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	if err := f.st.Pool.QueryRow(ctx, `SELECT applied_revision_id FROM environments WHERE workspace_id=$1 AND id=$2`, ws, databaseEnv.Id).Scan(&applied); err != nil || applied != view.Plan.TargetRevisionID {
		t.Fatal("primary restore receipt prematurely published a global target", err)
	}
	holdRemoteResume.Store(false)
	wait(func() bool {
		var state string
		if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, restoreOperation.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "failed" || state == "needs_attention" {
			t.Fatal("restore writer reconciliation did not finish", state)
		}
		return state == "succeeded"
	})
	if err := f.st.Pool.QueryRow(ctx, `SELECT applied_revision_id FROM environments WHERE workspace_id=$1 AND id=$2`, ws, databaseEnv.Id).Scan(&applied); err != nil || applied != restoreDraft.Id {
		t.Fatal("complete restore receipts did not publish the target revision", err)
	}
	var archive api.WorkspaceArchive
	passphrase := "cross-host fixture archive passphrase"
	b.call("POST", base+"/export", map[string]string{"passphrase": passphrase}, 200, &archive)
	// Revocation must overtake an unrelated uncertain leaf on an online peer.
	uncertainID := uuid.New()
	uncertain := api.ExecutionCommand{Id: uncertainID, ServerId: servers[1].Server.Id, EnvironmentId: unrelated.Id, ResourceId: uuid.New(), Generation: 900001, Kind: "test", Action: "inspect", PlanHash: strings.Repeat("b", 64), Deadline: time.Now().Add(time.Hour)}
	rawUncertain, _ := json.Marshal(uncertain)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,$7,'test','needs_attention')`, uncertainID, ws, uncertain.ServerId, unrelated.Id, uncertain.Generation, uuid.New(), rawUncertain); err != nil {
		t.Fatal(err)
	}
	// A transport replacement must revoke the predecessor while busy, then
	// install the new peer at a fresh generation once the host is idle.
	b.call("GET", base+"/network", nil, 200, &network)
	members[0].Endpoint = "192.0.2.9:51820"
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", BaseGeneration: network.Generation, Members: members}, 200, &network)
	wait(func() bool {
		var pending bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation<generation AND NOT COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, uncertain.ServerId, servers[0].Server.Id.String()).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending
	})
	b.call("GET", base+"/network", nil, 200, &network)
	if network.State == "ready" {
		t.Fatal("partial restriction was presented as full convergence")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='failed' WHERE id=$1`, uncertainID); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		var converged bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation=generation AND COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text,'endpoint',$4::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, uncertain.ServerId, servers[0].Server.Id.String(), members[0].Endpoint).Scan(&converged); err != nil {
			t.Fatal(err)
		}
		return converged
	})
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='needs_attention' WHERE id=$1`, uncertainID); err != nil {
		t.Fatal(err)
	}
	revokePrimary.Store(true)
	b.call("POST", base+"/servers/"+servers[0].Server.Id.String()+"/revoke", nil, 204, nil)
	wait(func() bool {
		var removed bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation=generation AND NOT COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, servers[1].Server.Id, servers[0].Server.Id.String()).Scan(&removed); err != nil {
			t.Fatal(err)
		}
		return removed
	})
	var uncertainState string
	if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, uncertainID).Scan(&uncertainState); err != nil || uncertainState != "needs_attention" {
		t.Fatal("network restriction discarded the unrelated uncertainty fence", err)
	}
	cancel()
	wg.Wait()
	destinationInstallation := setup(t)
	owner := destinationInstallation.browser(t)
	owner.login("owner@example.test", destinationInstallation.password)
	var imported api.Workspace
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": archive.Archive, "passphrase": passphrase}, 201, &imported)
	owner.call("GET", base+"/network", nil, 200, &network)
	if imported.Id != ws || network.State == "ready" {
		t.Fatal("import changed identity or renewed private link approval")
	}
	for index, server := range servers {
		var ticket api.TransferTicket
		owner.call("POST", base+"/servers/"+server.Server.Id.String()+"/transfer", nil, 201, &ticket)
		input := enrollments[index]
		input.Token, input.ControllerGeneration = ticket.Token, &ticket.Generation
		input.Credential, err = cryptobox.Token()
		if err != nil {
			t.Fatal(err)
		}
		inventory := api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}}
		unitPlans.Range(func(key, value any) bool {
			plan := value.(deployment.Plan)
			if plan.ServerID == server.Server.Id {
				inventory.Units[plan.UnitID.String()] = plan.Hash
			}
			return true
		})
		if index == 0 {
			inventory.Schedules[policy.ID.String()] = policy.Generation
			inventory.Schedules[recovery.ID.String()] = recovery.Generation
		}
		if len(inventory.Units) != 2 {
			t.Fatal("transfer fixture lost a serving unit")
		}
		input.Inventory = &inventory
		var withheldID, withheldHash string
		for id, hash := range inventory.Units {
			withheldID, withheldHash = id, hash
			delete(inventory.Units, id)
			break
		}
		agentRequest(t, destinationInstallation.server.Client(), destinationInstallation.server.URL, "/agent/v1/enroll", "", input, 409, nil)
		inventory.Units[withheldID] = withheldHash
		agentRequest(t, destinationInstallation.server.Client(), destinationInstallation.server.URL, "/agent/v1/enroll", "", input, 200, nil)
		agentRequest(t, destinationInstallation.server.Client(), destinationInstallation.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	}
}

func TestManagedNetworkPartialRestrictionConvergesAfterHostIdle(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	members := []api.ManagedNetworkMemberInput{}
	ids := []uuid.UUID{}
	var stopPrimary context.CancelFunc
	primaryDone := make(chan struct{})
	for i := 0; i < 2; i++ {
		enrolled, in, token := enrollment(t, f, b, ws, "deployment")
		yes := true
		in.Capabilities.CrossHost = &yes
		in.Capabilities.Visual = &yes
		in.Preflight.CrossHostInventory = &api.CrossHostInventory{CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: hostnetwork.StableInventory(nil)}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		identity, err := f.execution.Authenticate(ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, enrolled.Server.Id)
		members = append(members, api.ManagedNetworkMemberInput{ServerId: enrolled.Server.Id, Endpoint: []string{"192.0.2.1:51820", "192.0.2.2:51820"}[i], ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: hostnetwork.StableInventory(nil)})
		host := hostnetwork.Host{Dir: t.TempDir()}
		hostCtx, stopHost := context.WithCancel(ctx)
		defer stopHost()
		if i == 0 {
			stopPrimary = stopHost
		}
		wg.Go(func() {
			if i == 0 {
				defer close(primaryDone)
			}
			last := time.Now()
			for hostCtx.Err() == nil {
				if time.Since(last) > 10*time.Second {
					if _, err := f.execution.Heartbeat(hostCtx, identity, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}); err != nil {
						if hostCtx.Err() == nil {
							t.Error(err)
						}
						return
					}
					last = time.Now()
				}
				poll, done := context.WithTimeout(hostCtx, time.Second)
				c, err := f.execution.Poll(poll, identity)
				expired := poll.Err()
				done()
				if err != nil {
					if expired != nil || hostCtx.Err() != nil {
						continue
					}
					t.Error(err)
					return
				}
				if c == nil {
					continue
				}
				if c.Kind != "network" {
					time.Sleep(30 * time.Millisecond)
					continue
				}
				var command hostnetwork.Command
				if hostnetwork.Decode(*c.Network, &command) != nil {
					t.Error("invalid network command")
					return
				}
				key, err := host.Identity(command.MembershipID)
				if err != nil {
					t.Error(err)
					return
				}
				receipt := hostnetwork.Receipt{NetworkID: command.NetworkID, Identity: key}
				if command.Manifest != nil {
					receipt.Generation = command.Manifest.Generation
					receipt.Hash = hostnetwork.Hash(*command.Manifest)
					receipt.Disabled = command.Manifest.Disabled
				}
				raw, _ := json.Marshal(receipt)
				message := json.RawMessage(raw)
				if err = f.execution.Report(hostCtx, identity, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: &message}); err != nil && hostCtx.Err() == nil {
					t.Error(err)
					return
				}
			}
		})
	}
	var network struct {
		Generation int64  `json:"generation"`
		State      string `json:"state"`
	}
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", Members: members}, 200, &network)
	waitNetwork := func(check func() bool) {
		t.Helper()
		until := time.Now().Add(35 * time.Second)
		for time.Now().Before(until) {
			if check() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		var diagnostic []byte
		_ = f.st.Pool.QueryRow(ctx, `SELECT jsonb_build_object('members',(SELECT jsonb_agg(jsonb_build_object('server',server_id,'generation',generation,'ack',acknowledged_generation,'installed',installed_manifest)) FROM network_members WHERE workspace_id=$1),'operations',(SELECT jsonb_agg(jsonb_build_object('kind',o.kind,'state',o.state,'note',o.note,'action',o.command->'network'->>'action','errors',j.errors)) FROM execution_operations o LEFT JOIN river_job j ON j.id=o.job_id WHERE o.workspace_id=$1))`, ws).Scan(&diagnostic)
		t.Fatalf("network did not converge: %s", diagnostic)
	}
	waitNetwork(func() bool { b.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
	uncertain := uuid.New()
	c := api.ExecutionCommand{Id: uncertain, ServerId: ids[1], Kind: "test", ResourceId: uuid.New(), Generation: 9999, PlanHash: strings.Repeat("b", 64), Action: "inspect", Deadline: time.Now().Add(time.Hour)}
	raw, _ := json.Marshal(c)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,9999,$4,$5,'test','needs_attention')`, uncertain, ws, ids[1], uuid.New(), raw); err != nil {
		t.Fatal(err)
	}
	members[0].Endpoint = "192.0.2.9:51820"
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", BaseGeneration: network.Generation, Members: members}, 200, &network)
	waitNetwork(func() bool {
		var pending bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation<generation AND NOT COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, ids[1], ids[0].String()).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending
	})
	b.call("GET", base+"/network", nil, 200, &network)
	if network.State == "ready" {
		t.Fatal("restriction falsely reported full convergence")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='failed' WHERE id=$1`, uncertain); err != nil {
		t.Fatal(err)
	}
	waitNetwork(func() bool {
		var complete bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation=generation AND COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text,'endpoint',$4::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, ids[1], ids[0].String(), members[0].Endpoint).Scan(&complete); err != nil {
			t.Fatal(err)
		}
		return complete
	})
	stopPrimary()
	<-primaryDone
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='needs_attention' WHERE id=$1`, uncertain); err != nil {
		t.Fatal(err)
	}
	b.call("POST", base+"/servers/"+ids[0].String()+"/revoke", nil, 204, nil)
	waitNetwork(func() bool {
		var removed bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation=generation AND NOT COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, ids[1], ids[0].String()).Scan(&removed); err != nil {
			t.Fatal(err)
		}
		return removed
	})
	var uncertainState string
	if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, uncertain).Scan(&uncertainState); err != nil || uncertainState != "needs_attention" {
		t.Fatal("peer revocation discarded unrelated uncertainty", err)
	}
}
