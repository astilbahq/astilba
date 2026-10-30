package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

func queuedMaintenanceResume(t *testing.T, phase string, primaryState, childState api.ExecutionState) preparationFixture {
	t.Helper()
	f := queuedPreparation(t, primaryState, childState)
	ctx := context.Background()
	for _, id := range []uuid.UUID{f.primary, f.child} {
		var raw []byte
		if err := f.service.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var c api.ExecutionCommand
		var body deployment.CrossHostCommand
		if json.Unmarshal(raw, &c) != nil || json.Unmarshal(*c.Crosshost, &body) != nil {
			t.Fatal("invalid fixture command")
		}
		body.Phase = phase
		body.Maintenance = &deployment.Maintenance{Kind: "backup", ResourceID: uuid.New()}
		message, _ := json.Marshal(body)
		payload := json.RawMessage(message)
		c.Crosshost, c.PlanHash = &payload, body.Digest()
		// The coordinator freezes all children before the provider finishes.
		// Delivery can outlive the original deadline without replacing them.
		c.Deadline = time.Now().Add(-time.Hour)
		raw, _ = json.Marshal(c)
		if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET command=$2 WHERE id=$1`, id, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.st.Pool.Exec(ctx, `UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, f.parent, "maintenance-"+phase); err != nil {
		t.Fatal(err)
	}
	if err := f.service.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=$1`, f.child).Scan(&f.frozen); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMaintenanceResumeWaitsForPrimaryRecovery(t *testing.T) {
	for _, phase := range []string{"resume-source", "resume-target"} {
		t.Run(phase, func(t *testing.T) {
			f := queuedMaintenanceResume(t, phase, "running", "queued")
			ctx := context.Background()
			// Cancellation cannot strand a writer-resume, including a consumer
			// which has never received its already-frozen command.
			if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true WHERE id=ANY($1)`, []uuid.UUID{f.parent, f.child}); err != nil {
				t.Fatal(err)
			}
			if err := f.service.execution.Start(ctx); err != nil {
				t.Fatal(err)
			}
			for _, primary := range []api.ExecutionState{"running", "needs_attention", "failed", "cancelled"} {
				if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET state=$2 WHERE id=$1`, f.primary, primary); err != nil {
					t.Fatal(err)
				}
				f.wait(t, func(state api.ExecutionState, _ string) bool { return state == "queued" })
			}
			if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='succeeded' WHERE id=$1`, f.primary); err != nil {
				t.Fatal(err)
			}
			f.wait(t, func(state api.ExecutionState, _ string) bool { return state == "dispatched" })
			poll, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			c, err := f.service.execution.Poll(poll, f.identity)
			if err != nil || c == nil || c.Id != f.child || !c.CancelRequested {
				t.Fatal("confirmed primary did not release the frozen cancelled resumption", err)
			}
		})
	}
}

func TestMaintenanceResumeKeepsDispatchedReconciliation(t *testing.T) {
	for _, phase := range []string{"resume-source", "resume-target"} {
		t.Run(phase, func(t *testing.T) {
			f := queuedMaintenanceResume(t, phase, "needs_attention", "dispatched")
			ctx := context.Background()
			if _, err := f.service.st.Pool.Exec(ctx, `UPDATE cross_host_operations SET phase='maintenance-complete-source' WHERE id=$1`, f.parent); err != nil {
				t.Fatal(err)
			}
			if err := f.service.execution.Start(ctx); err != nil {
				t.Fatal(err)
			}
			f.wait(t, func(state api.ExecutionState, _ string) bool { return state == "dispatched" })
			poll, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			c, err := f.service.execution.Poll(poll, f.identity)
			if err != nil || c == nil || c.Id != f.child {
				t.Fatal("dispatched resumption lost reconciliation delivery", err)
			}
		})
	}
}

func TestMaintenanceResumeDoesNotReleaseOutsideItsParentPhase(t *testing.T) {
	f := queuedMaintenanceResume(t, "resume-source", "succeeded", "queued")
	ctx := context.Background()
	if _, err := f.service.st.Pool.Exec(ctx, `UPDATE cross_host_operations SET phase='maintenance-resume-target' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.execution.Start(ctx); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(state api.ExecutionState, _ string) bool { return state == "queued" })
}

func TestMaintenancePrimaryTerminalFailureSurfacesBeforeConsumerCompletion(t *testing.T) {
	for _, phase := range []string{"resume-source", "resume-target"} {
		for _, primaryState := range []api.ExecutionState{"failed", "cancelled"} {
			t.Run(phase+"/"+string(primaryState), func(t *testing.T) {
				f := queuedMaintenanceResume(t, phase, primaryState, "queued")
				ctx := context.Background()
				var parent api.ExecutionCommand
				var raw []byte
				var ws uuid.UUID
				if err := f.service.st.Pool.QueryRow(ctx, `SELECT command,workspace_id FROM execution_operations WHERE id=$1`, f.parent).Scan(&raw, &ws); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &parent); err != nil {
					t.Fatal(err)
				}
				type unitIntent struct {
					Unit   deployment.DistributedUnit `json:"unit"`
					Intent deployment.Maintenance     `json:"intent"`
				}
				units, intents := []deployment.DistributedUnit{}, []unitIntent{}
				for _, id := range []uuid.UUID{f.primary, f.child} {
					var child api.ExecutionCommand
					var body deployment.CrossHostCommand
					if err := f.service.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=$1`, id).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					if json.Unmarshal(raw, &child) != nil || json.Unmarshal(*child.Crosshost, &body) != nil {
						t.Fatal("invalid child fixture")
					}
					child.Id = uuid.NewSHA1(parent.Id, []byte("crosshost:maintenance-"+phase+":"+body.Unit.Plan.UnitID.String()))
					child.ResourceId = body.Unit.Plan.UnitID
					raw, _ = json.Marshal(child)
					if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET id=$2,command=$3 WHERE id=$1`, id, child.Id, raw); err != nil {
						t.Fatal(err)
					}
					if id == f.child {
						f.child = child.Id
						if _, err := f.service.st.Pool.Exec(ctx, `UPDATE river_job SET args=jsonb_build_object('id',$2::text) WHERE id=$1`, f.job, child.Id.String()); err != nil {
							t.Fatal(err)
						}
					} else {
						f.primary = child.Id
					}
					units = append(units, body.Unit)
					intents = append(intents, unitIntent{Unit: body.Unit, Intent: *body.Maintenance})
					if _, err := f.service.st.Pool.Exec(ctx, `INSERT INTO cross_host_reservations(server_id,workspace_id,parent_id,cpu_millis,memory_mib) VALUES($1,$2,$3,0,0)`, child.ServerId, ws, parent.Id); err != nil {
						t.Fatal(err)
					}
				}
				plan := units[0].Plan
				plan.Distributed = &deployment.DistributedPlan{Units: units}
				// Match the frozen coordinator schema; host effects are separate.
				body := struct {
					Action      string          `json:"action"`
					Plan        deployment.Plan `json:"plan"`
					Maintenance any             `json:"maintenance,omitempty"`
				}{"maintenance", plan, struct {
					Effect struct{}     `json:"effect"`
					Units  []unitIntent `json:"units"`
				}{Units: intents}}
				payload, _ := json.Marshal(body)
				message := json.RawMessage(payload)
				parent.Crosshost, parent.PlanHash = &message, execution.Hash(body)
				id := uuid.New()
				envelope := []byte("frozen-test-capsule")
				d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: ws, ParentID: parent.Id, RootHash: parent.PlanHash, GroupID: uuid.New(), GroupHash: parent.PlanHash, BaseID: id, CheckpointID: id, Bytes: len(envelope), Digest: deployment.RecoveryDigest(envelope), Participants: []uuid.UUID{units[0].Plan.ServerID, units[1].Plan.ServerID}}
				descriptor, _ := json.Marshal(d)
				metadata := json.RawMessage(descriptor)
				parent.RecoveryCheckpoint = &metadata
				raw, _ = json.Marshal(parent)
				if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET command=$2 WHERE id=$1`, parent.Id, raw); err != nil {
					t.Fatal(err)
				}
				if _, err := f.service.st.Pool.Exec(ctx, `INSERT INTO cross_host_checkpoints(id,workspace_id,parent_id,sequence,descriptor,envelope,phase,acknowledged) VALUES($1,$2,$3,0,$4,$5,$6,true)`, id, ws, parent.Id, descriptor, envelope, "maintenance-"+phase); err != nil {
					t.Fatal(err)
				}
				job, err := f.service.execution.Queue.Insert(ctx, networkRemovalJob{ID: parent.Id}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET job_id=$2 WHERE id=$1`, parent.Id, job.Job.ID); err != nil {
					t.Fatal(err)
				}
				if err := f.service.execution.Start(ctx); err != nil {
					t.Fatal(err)
				}
				for _, consumer := range []api.ExecutionState{"queued", "cancelled"} {
					if consumer == "cancelled" {
						if _, err := f.service.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='cancelled' WHERE id=$1`, f.child); err != nil {
							t.Fatal(err)
						}
					}
					deadline := time.After(10 * time.Second)
					for {
						select {
						case event := <-f.events:
							if event.Job.ID != job.Job.ID {
								continue
							}
						case <-deadline:
							t.Fatal("parent did not report the primary failure")
						}
						var attention bool
						if err := f.service.st.Pool.QueryRow(ctx, `SELECT o.state='needs_attention' AND h.phase=$2 AND (SELECT count(*) FROM cross_host_reservations WHERE parent_id=o.id)=2 FROM execution_operations o JOIN cross_host_operations h ON h.id=o.id WHERE o.id=$1`, parent.Id, "maintenance-"+phase).Scan(&attention); err != nil || !attention {
							t.Fatal("primary terminal failure was hidden behind unfinished consumers or released reservations", err)
						}
						break
					}
				}
			})
		}
	}
}
