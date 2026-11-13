package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Capture real capsule bases on either side of the existing preview cleanup
// journey. Protocol peers provide the host inventories; PostgreSQL/River and
// the recovery journal use the production paths.
func preparePreviewRemovalRecovery(t *testing.T, f *fixture, b *browser, ws uuid.UUID) func(*testing.T, api.AgentEnrollment, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	prefix := "/api/v1/workspaces/" + ws.String()
	inventory := hostnetwork.StableInventory(nil)
	var inputs []api.AgentEnrollment
	var members []api.ManagedNetworkMemberInput
	var plans sync.Map
	var retained sync.Map
	var held atomic.Bool
	deliveries := make(chan recoveryOrderingDelivery, 1)
	for i := 0; i < 2; i++ {
		enrolled, input, token := enrollment(t, f, b, ws, "deployment")
		yes := true
		input.Capabilities.Visual = &yes
		input.Capabilities.CrossHost = &yes
		input.Capabilities.Replicas = &yes
		input.Capabilities.ServiceOutputs = &yes
		input.Capabilities.SafeReleases = &yes
		input.Preflight.CrossHostInventory = &api.CrossHostInventory{IngressReady: &yes, CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: inventory}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
		identity, err := f.execution.Authenticate(ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input)
		members = append(members, api.ManagedNetworkMemberInput{ServerId: input.ServerId, Endpoint: []string{"192.0.2.1:51820", "192.0.2.2:51820"}[i], ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: inventory})
		host := hostnetwork.Host{Dir: t.TempDir()}
		workers.Go(func() {
			var installed hostnetwork.Manifest
			scopes := map[uuid.UUID]deployment.Plan{}
			lastHeartbeat := time.Now()
			for ctx.Err() == nil {
				if time.Since(lastHeartbeat) > 10*time.Second {
					if _, err := f.execution.Heartbeat(ctx, identity, api.AgentHeartbeat{Capabilities: input.Capabilities, Preflight: input.Preflight}); err != nil {
						if ctx.Err() == nil {
							t.Error(err)
						}
						return
					}
					lastHeartbeat = time.Now()
				}
				poll, done := context.WithTimeout(ctx, time.Second)
				c, err := pollReleased(f.execution, poll, identity)
				pollErr := poll.Err()
				done()
				if err != nil {
					if waitForFixturePollLease(ctx, err) || pollErr != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
						continue
					}
					t.Error(err)
					return
				}
				if c == nil {
					continue
				}
				report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}
				switch c.Kind {
				case "network":
					receipt, err := fixtureNetworkReceipt(host, *c, &installed, scopes, 0)
					if err != nil {
						t.Error(err)
						return
					}
					raw, _ := stablejson.Marshal(receipt)
					report.Network = (*jsontext.Value)(&raw)
				case "visual":
					var payload deployment.Payload
					if json.Unmarshal(*c.Visual, &payload) != nil {
						t.Error("invalid visual command")
						return
					}
					observation := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}
					if payload.Action == "resolve" {
						observation.Images = map[string]string{}
						for _, a := range payload.Plan.Specification.Applications {
							observation.Images[a.ID.String()] = *a.Source.Image
						}
					} else {
						observation.Applied = true
						plans.Store(payload.Plan.UnitID, payload.Plan)
						for _, a := range payload.Plan.Specification.Applications {
							observation.Resources = append(observation.Resources, deployment.Resource{ID: a.ID, State: "running", Image: payload.Plan.Images[a.ID.String()]})
						}
					}
					raw, _ := stablejson.Marshal(observation)
					report.Observation = (*jsontext.Value)(&raw)
				case "crosshost":
					var body deployment.CrossHostCommand
					if json.Unmarshal(*c.Crosshost, &body) != nil {
						t.Error("invalid cross-host command")
						return
					}
					scopes[body.ParentID] = body.Unit.Plan
					receipt := deployment.CrossHostReceipt{ParentID: body.ParentID, GroupHash: body.GroupHash, UnitID: body.Unit.Plan.UnitID, PlanHash: body.Unit.Plan.Hash, Phase: body.Phase, Observation: deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}}
					if body.Phase == "checkpoint" {
						var d deployment.RecoveryDescriptor
						if c.RecoveryCheckpoint == nil || json.Unmarshal(*c.RecoveryCheckpoint, &d) != nil {
							t.Error("checkpoint missing")
							return
						}
						envelope, err := f.execution.RecoveryCheckpoint(ctx, identity, d.ParentID, d.CheckpointID)
						if err != nil {
							t.Error(err)
							return
						}
						retained.Store(c.Id, recoveryOrderingDelivery{Command: *c, Envelope: envelope})
						if held.Load() && d.Sequence == 0 {
							select {
							case deliveries <- recoveryOrderingDelivery{Command: *c, Envelope: envelope}:
							default:
							}
							time.Sleep(20 * time.Millisecond)
							continue
						}
						receipt.CheckpointHash = d.Digest
					}
					if body.Phase == "resolve" {
						receipt.Observation.Images = map[string]string{}
						for _, a := range body.Unit.Plan.LocalApplications() {
							receipt.Observation.Images[a.ID.String()] = *a.Source.Image
						}
					}
					if body.Phase == "complete" {
						receipt.Observation.Applied = true
						plans.Store(body.Unit.Plan.UnitID, body.Unit.Plan)
						for _, a := range body.Unit.Plan.LocalApplications() {
							r := deployment.Resource{ID: a.ID, State: "running", Image: body.Unit.Plan.Images[a.ID.String()]}
							for _, id := range body.Unit.Plan.InstanceIDs(a.ID) {
								r.Instances = append(r.Instances, deployment.InstanceState{InstanceID: id, State: "running"})
							}
							receipt.Observation.Resources = append(receipt.Observation.Resources, r)
						}
					}
					raw, _ := stablejson.Marshal(receipt)
					report.Crosshost = (*jsontext.Value)(&raw)
				default:
					t.Error("unexpected recovery fixture command", c.Kind)
					return
				}
				if err = f.execution.Report(ctx, identity, report); err != nil {
					if ctx.Err() == nil {
						t.Error(err)
					}
					return
				}
			}
		})
	}
	wait := func(t *testing.T, test func() bool) {
		t.Helper()
		deadline := time.Now().Add(70 * time.Second)
		for time.Now().Before(deadline) {
			if test() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("cleanup recovery fixture timed out")
	}
	var network struct {
		ID    uuid.UUID `json:"id"`
		State string    `json:"state"`
	}
	b.call("PUT", prefix+"/network", api.ConfigureManagedNetwork{Prefix: "10.250.0.0/24", Members: members}, 200, &network)
	wait(t, func() bool { b.call("GET", prefix+"/network", nil, 200, &network); return network.State == "ready" })
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Recovery roots"}, 201, &project)
	base := prefix + "/projects/" + project.Id.String() + "/environments"
	var env api.Environment
	b.call("POST", base, map[string]string{"name": "Root"}, 201, &env)
	path := base + "/" + env.Id.String()
	app := uuid.New()
	image := execution.TestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}}
	spec.Routes = []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "recovery-root.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}
	current := env.DraftRevisionId
	apply := func(t *testing.T, hold bool) (api.Operation, recoveryOrderingDelivery) {
		t.Helper()
		wait(t, func() bool { b.call("GET", prefix+"/network", nil, 200, &network); return network.State == "ready" })
		b.login("owner@example.test", f.password)
		var draft api.Revision
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": current, "specification": spec}, 200, &draft)
		current = draft.Id
		var status deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: current, ServerId: inputs[0].ServerId}, 201, &status)
		wait(t, func() bool {
			b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
			return status.Plan != nil
		})
		held.Store(hold)
		var op api.Operation
		b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
		if hold {
			deadline := time.NewTimer(70 * time.Second)
			defer deadline.Stop()
			for {
				select {
				case d := <-deliveries:
					if d.Command.ParentId != nil && *d.Command.ParentId == op.Id {
						return op, d
					}
				case <-deadline.C:
					t.Fatal("checkpoint not delivered for parent", op.Id)
				}
			}
		}
		wait(t, func() bool {
			var state string
			if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, op.Id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state == "failed" {
				t.Fatal("root fixture failed")
			}
			return state == "succeeded"
		})
		return op, recoveryOrderingDelivery{}
	}
	apply(t, false)
	spec.Applications[0].Replicas = 2
	spec.Placement = &domain.Placement{NetworkID: network.ID, IngressServerID: inputs[0].ServerId, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 100, MemoryMiB: 128, Targets: []domain.PlacementTarget{{ServerID: inputs[0].ServerId, Count: 1}, {ServerID: inputs[1].ServerId, Count: 1}}}}}
	op, _ := apply(t, true)
	held.Store(false)
	wait(t, func() bool {
		var state string
		if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, op.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state == "succeeded"
	})
	checkpoints := func(parent uuid.UUID) []recoveryOrderingDelivery {
		var chain []recoveryOrderingDelivery
		retained.Range(func(_, value any) bool {
			v := value.(recoveryOrderingDelivery)
			if v.Command.ParentId != nil && *v.Command.ParentId == parent {
				chain = append(chain, v)
			}
			return true
		})
		sort.Slice(chain, func(i, j int) bool {
			var a, b deployment.RecoveryDescriptor
			_ = json.Unmarshal(*chain[i].Command.RecoveryCheckpoint, &a)
			_ = json.Unmarshal(*chain[j].Command.RecoveryCheckpoint, &b)
			return a.Sequence < b.Sequence
		})
		return chain
	}
	earlier := checkpoints(op.Id)
	before := captureRecoveryOrderingSnapshot(t, f, ws)
	return func(t *testing.T, previewHost api.AgentEnrollment, request, previewEnv uuid.UUID) {
		t.Helper()
		op, later := apply(t, true)
		after := captureRecoveryOrderingSnapshot(t, f, ws)
		var wantedRemoval uuid.UUID
		for _, row := range after.Tables["environments"] {
			var id uuid.UUID
			_ = json.Unmarshal(row["id"], &id)
			if id == previewEnv {
				_ = json.Unmarshal(row["runtime_operation_id"], &wantedRemoval)
			}
		}
		if wantedRemoval == uuid.Nil {
			t.Fatal("source did not retain the cleanup event")
		}
		verify := func(destination *fixture, snapshot portability.Archive) {
			var requestEnvironment *uuid.UUID
			var requestGeneration int64
			var requestHead string
			for _, row := range snapshot.Tables["preview_requests"] {
				var id uuid.UUID
				_ = json.Unmarshal(row["id"], &id)
				if id == request {
					_ = json.Unmarshal(row["environment_id"], &requestEnvironment)
					_ = json.Unmarshal(row["generation"], &requestGeneration)
					_ = json.Unmarshal(row["head"], &requestHead)
				}
			}
			var revision *uuid.UUID
			var event uuid.UUID
			var outcome, state string
			if err := destination.st.Pool.QueryRow(ctx, `SELECT e.applied_revision_id,e.runtime_operation_id,o.runtime_outcome FROM environments e JOIN execution_operations o ON o.id=e.runtime_operation_id WHERE e.workspace_id=$1 AND e.id=$2`, ws, previewEnv).Scan(&revision, &event, &outcome); err != nil || revision != nil || outcome != "removed" || event != wantedRemoval {
				t.Fatal("removed preview outcome was not restored", err, revision, event, outcome)
			}
			var restoredEnvironment *uuid.UUID
			var restoredGeneration int64
			var restoredHead string
			if err := destination.st.Pool.QueryRow(ctx, `SELECT environment_id,generation,head,state FROM preview_requests WHERE workspace_id=$1 AND id=$2`, ws, request).Scan(&restoredEnvironment, &restoredGeneration, &restoredHead, &state); err != nil {
				t.Fatal(err)
			}
			sameEnvironment := restoredEnvironment == nil && requestEnvironment == nil || restoredEnvironment != nil && requestEnvironment != nil && *restoredEnvironment == *requestEnvironment
			expectedState := "blocked"
			if requestEnvironment != nil && *requestEnvironment == previewEnv {
				expectedState = "deleted"
			}
			if !sameEnvironment || restoredGeneration != requestGeneration || restoredHead != requestHead || state != expectedState {
				t.Fatal("historical cleanup changed the current preview request", restoredEnvironment, restoredGeneration, restoredHead, state)
			}
			if requestEnvironment != nil && *requestEnvironment != previewEnv {
				replacementSecrets := 0
				for _, row := range snapshot.Tables["secrets"] {
					var owner uuid.UUID
					_ = json.Unmarshal(row["environment_id"], &owner)
					if owner == *requestEnvironment {
						replacementSecrets++
					}
				}
				if replacementSecrets == 0 {
					t.Fatal("replacement secrets missing from snapshot")
				}
				var count int
				if err := destination.st.Pool.QueryRow(ctx, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=$2`, ws, *requestEnvironment).Scan(&count); err != nil || count != replacementSecrets {
					t.Fatal("historical cleanup changed replacement secrets", count, err)
				}
			}
			for _, table := range []string{"visual_runtime", "route_claims", "secrets"} {
				var count int
				if err := destination.st.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1 AND environment_id=$2", ws, previewEnv).Scan(&count); err != nil || count != 0 {
					t.Fatal("cleanup rows returned", table, count, err)
				}
			}
			var count int
			// Production/template secrets survive; preview secret IDs from the older
			// base must be absent even when immutable archive rows were merged.
			for _, row := range before.Tables["secrets"] {
				var owner, id uuid.UUID
				_ = json.Unmarshal(row["environment_id"], &owner)
				_ = json.Unmarshal(row["id"], &id)
				if owner == previewEnv {
					for _, version := range before.Tables["secret_versions"] {
						var secret, versionID uuid.UUID
						_ = json.Unmarshal(version["secret_id"], &secret)
						_ = json.Unmarshal(version["id"], &versionID)
						if secret == id {
							if err := destination.st.Pool.QueryRow(ctx, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2`, ws, versionID).Scan(&count); err != nil || count != 0 {
								t.Fatal("preview secret version returned", err, count)
							}
						}
					}
				}
			}
		}
		verifyRecoveryOrdering(t, f, ws, before, after, later, append(inputs, previewHost), &plans, time.Time{}, time.Time{}, 0, 0, recoveryOrderingExtra{
			Deliveries: earlier,
			Verify: func(destination *fixture, snapshot portability.Archive, exports []string) {
				verify(destination, snapshot)
				// A fresh inspection is distinct from retrying the original state hash.
				input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: destination.owner, HostExports: exports}
				preview, err := destination.execution.RecoverCrossHost(ctx, input)
				if err != nil {
					t.Fatal("inspect recovery after preview cleanup", err)
				}
				input.ExpectedState = preview.StateHash
				applied, err := destination.execution.RecoverCrossHost(ctx, input)
				if err != nil || !applied.Applied {
					t.Fatal("fresh recovery after preview cleanup", err)
				}
				verify(destination, snapshot)
			},
		})
		held.Store(false)
		wait(t, func() bool {
			var state string
			if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, op.Id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state == "failed" || state == "cancelled" {
				t.Fatal("root fixture did not complete", state)
			}
			return state == "succeeded"
		})
		earlier = append(earlier, checkpoints(op.Id)...)
	}
}
