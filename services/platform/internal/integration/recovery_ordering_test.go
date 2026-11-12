package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/client"
)

type recoveryOrderingExtra struct {
	Deliveries []recoveryOrderingDelivery
	Verify     func(*fixture, portability.Archive, []string)
}

type recoveryOrderingDelivery struct {
	Command  api.ExecutionCommand
	Envelope []byte
}

func captureRecoveryOrderingSnapshot(t *testing.T, f *fixture, ws uuid.UUID) portability.Archive {
	t.Helper()
	tx, err := f.st.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	raw, err := portability.CaptureRecovery(context.Background(), tx, ws)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := portability.DecodeRecovery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func verifyRecoveryOrdering(t *testing.T, source *fixture, ws uuid.UUID, before, after portability.Archive, delivery recoveryOrderingDelivery, enrollments []api.AgentEnrollment, plans *sync.Map, draftAt, targetAt time.Time, oldGeneration, newGeneration int64, extras ...recoveryOrderingExtra) {
	t.Helper()
	ctx := context.Background()
	journal, err := agent.Open(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	var primary api.AgentEnrollment
	for _, enrollment := range enrollments {
		if enrollment.ServerId == delivery.Command.ServerId {
			primary = enrollment
		}
	}
	if err = journal.SetConfig(agent.Config{Origin: "https://source.example.test", ServerID: primary.ServerId, MachineID: primary.MachineId, Credential: *primary.Credential, Role: "deployment", Enrolled: true}); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	// Only the metadata command runs locally. Workload inventories below belong
	// to the same controlled protocol peers as the surrounding SQL/River fixture.
	engine := agent.Engine{Journal: journal, Docker: docker, FetchCheckpoint: func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return delivery.Envelope, nil }}
	for _, extra := range extras {
		for _, earlier := range extra.Deliveries {
			engine.FetchCheckpoint = func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return earlier.Envelope, nil }
			if err = engine.Execute(ctx, earlier.Command); err != nil {
				t.Fatal("retain earlier checkpoint", err)
			}
			record, err := journal.Record(earlier.Command.Id)
			if err != nil {
				t.Fatal(err)
			}
			if err = journal.Acknowledge(earlier.Command.Id, record.Report.Sequence); err != nil {
				t.Fatal(err)
			}
		}
	}
	engine.FetchCheckpoint = func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return delivery.Envelope, nil }
	if err = engine.Execute(ctx, delivery.Command); err != nil {
		t.Fatal("retain actual management checkpoint", err)
	}
	var exports []string
	retainedBytes := map[string][32]byte{}
	for _, enrollment := range enrollments {
		dir := filepath.Join(t.TempDir(), "export")
		evidence := deployment.RecoveryEvidence{Version: 1, ServerID: enrollment.ServerId, MachineID: enrollment.MachineId, Units: map[string]string{}, Schedules: map[string]int64{}, Roots: []deployment.RecoveryRoot{}, Networks: []hostnetwork.Manifest{}}
		if enrollment.ServerId == primary.ServerId {
			if err = journal.ExportRecovery(dir); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(dir, "*.capsule"))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				retainedBytes[file] = sha256.Sum256(raw)
			}
			evidence, err = journal.RecoveryEvidence()
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if err = os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			empty := sha256.Sum256(nil)
			evidence.ProofDigest = hex.EncodeToString(empty[:])
			if err = os.WriteFile(filepath.Join(dir, "proofs.jsonl"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		plans.Range(func(_, value any) bool {
			plan := value.(deployment.Plan)
			if plan.ServerID == enrollment.ServerId {
				evidence.Units[plan.UnitID.String()] = plan.Hash
			}
			return true
		})
		rows, err := source.st.Pool.Query(ctx, `SELECT id,status FROM backup_policies WHERE workspace_id=$1 AND server_id=$2 AND status ? 'policyId'`, ws, enrollment.ServerId)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id uuid.UUID
			var raw []byte
			var status struct {
				Generation int64 `json:"generation"`
			}
			if err = rows.Scan(&id, &raw); err != nil || json.Unmarshal(raw, &status) != nil {
				t.Fatal("read accepted schedule inventory", err)
			}
			evidence.Schedules[id.String()] = status.Generation
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		var raw []byte
		var manifest hostnetwork.Manifest
		if err = source.st.Pool.QueryRow(ctx, `SELECT installed_manifest FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, enrollment.ServerId).Scan(&raw); err != nil && !errors.Is(err, pgx.ErrNoRows) || err == nil && (json.Unmarshal(raw, &manifest) != nil || manifest.Validate() != nil) {
			t.Fatal("read accepted network inventory", err)
		}
		if err == nil {
			evidence.Networks = []hostnetwork.Manifest{manifest}
		}
		raw, err = stablejson.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "host.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		exports = append(exports, dir)
	}
	recover := func(snapshot portability.Archive) error {
		destination := setup(t)
		destination.server.Close()
		if err := destination.execution.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		destination.st.Keys = source.st.Keys
		if _, err := destination.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Ordering recovery')`, ws); err != nil {
			t.Fatal(err)
		}
		if _, err := destination.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, ws, destination.owner); err != nil {
			t.Fatal(err)
		}
		tx, err := destination.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = portability.MergeRecovery(ctx, tx, snapshot, destination.owner); err != nil {
			tx.Rollback(ctx)
			t.Fatal("restore management snapshot", err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = destination.st.Pool.Exec(ctx, `INSERT INTO troubleshooting_policies(workspace_id,environment_id,enabled,owners_only,generation,updated_by) SELECT workspace_id,id,true,false,1,$2 FROM environments WHERE workspace_id=$1`, ws, destination.owner); err != nil {
			t.Fatal(err)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: destination.owner, HostExports: exports}
		preview, err := destination.execution.RecoverCrossHost(ctx, input)
		if err != nil {
			return fmt.Errorf("preview: %w", err)
		}
		input.ExpectedState = preview.StateHash
		applied, err := destination.execution.RecoverCrossHost(ctx, input)
		if err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		if err == nil && !applied.Applied {
			t.Fatal("recovery did not apply the reviewed checkpoint")
		}
		var sessionsEnabled int
		if err := destination.st.Pool.QueryRow(ctx, `SELECT count(*) FROM troubleshooting_policies WHERE workspace_id=$1 AND enabled`, ws).Scan(&sessionsEnabled); err != nil || sessionsEnabled != 0 {
			t.Fatal("recovery left terminal sessions enabled", sessionsEnabled, err)
		}
		if err == nil {
			var wantRevision, wantOperation uuid.UUID
			var wantPlan uuid.UUID
			for _, row := range after.Tables["environments"] {
				var id uuid.UUID
				_ = json.Unmarshal(row["id"], &id)
				if id == delivery.Command.EnvironmentId {
					_ = json.Unmarshal(row["applied_revision_id"], &wantRevision)
					_ = json.Unmarshal(row["runtime_operation_id"], &wantOperation)
				}
			}
			for _, row := range after.Tables["visual_runtime"] {
				var id uuid.UUID
				_ = json.Unmarshal(row["environment_id"], &id)
				if id == delivery.Command.EnvironmentId {
					_ = json.Unmarshal(row["plan_id"], &wantPlan)
				}
			}
			var revision, operation, plan uuid.UUID
			if err := destination.st.Pool.QueryRow(ctx, `SELECT e.applied_revision_id,e.runtime_operation_id,r.plan_id FROM environments e JOIN visual_runtime r ON r.workspace_id=e.workspace_id AND r.environment_id=e.id WHERE e.workspace_id=$1 AND e.id=$2`, ws, delivery.Command.EnvironmentId).Scan(&revision, &operation, &plan); err != nil || wantOperation == uuid.Nil || revision != wantRevision || operation != wantOperation || plan != wantPlan {
				t.Fatal("recovery did not adopt the complete applied tuple", err, revision, operation, plan)
			}
		}
		if err == nil {
			for _, extra := range extras {
				if extra.Verify != nil {
					extra.Verify(destination, snapshot, exports)
				}
			}
		}
		return err
	}
	if err = recover(after); err != nil {
		t.Fatal("control recovery with the current applied source", err)
	}
	t.Log("control recovery with the current applied source succeeded")
	if err = recover(before); err != nil {
		t.Fatalf("newer applied source was rejected: draft created %s, upgrade target created %s, generation %d -> %d: %v", draftAt.Format(time.RFC3339Nano), targetAt.Format(time.RFC3339Nano), oldGeneration, newGeneration, err)
	}
	for path, want := range retainedBytes {
		raw, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(raw) != want {
			t.Fatal("recovery changed retained checkpoint bytes", err)
		}
	}

}
