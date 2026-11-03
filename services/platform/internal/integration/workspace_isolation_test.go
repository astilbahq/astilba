package integration

import (
	"context"
	"errors"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PLAT-119: recovery writes rows named by a checkpoint, and several tables key
// conflicts on a global identifier. A write naming another workspace must not
// create, take over or change that workspace's rows.
func TestRecoveryWritesStayInWorkspace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, _, parent := seedDiagnosticHistory(t, f)
	other := uuid.New()
	if _, e := f.st.Pool.Exec(ctx, "INSERT INTO workspaces(id,name) VALUES($1,'Other workspace')", other); e != nil {
		t.Fatal(e)
	}
	var server uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "SELECT server_id FROM execution_operations WHERE id=$1", parent).Scan(&server); e != nil {
		t.Fatal(e)
	}
	q := db.New(f.st.Pool)
	recovery := func(workspace uuid.UUID, source string) (int64, error) {
		return q.UpsertCrossHostRecovery(ctx, db.UpsertCrossHostRecoveryParams{SourceHash: source, InputHash: "input", OwnerID: f.owner, WorkspaceID: workspace, ParentID: parent})
	}
	stage := func(workspace uuid.UUID, generation int64) (int64, error) {
		return q.StageRecoveryServer(ctx, db.StageRecoveryServerParams{ParentID: parent, WorkspaceID: workspace, ServerID: server, MachineID: uuid.New(), ControllerGeneration: generation, Inventory: []byte(`{}`), Handover: []byte(`{}`)})
	}
	reserve := func(workspace uuid.UUID, cpu int64) (int64, error) {
		return q.ReserveRecoveredServer(ctx, db.ReserveRecoveredServerParams{ServerID: server, WorkspaceID: workspace, ParentID: parent, CpuMillis: cpu, MemoryMib: 64})
	}
	for _, step := range []struct {
		name   string
		upsert func() (int64, error)
		rows   int64
	}{
		{"foreign recovery of an unrecorded root", func() (int64, error) { return recovery(other, "foreign") }, 0},
		{"recovery", func() (int64, error) { return recovery(ws, "owned") }, 1},
		{"foreign recovery", func() (int64, error) { return recovery(other, "foreign") }, 0},
		{"stage", func() (int64, error) { return stage(ws, 7) }, 1},
		{"foreign stage", func() (int64, error) { return stage(other, 9) }, 0},
		{"reservation", func() (int64, error) { return reserve(ws, 100) }, 1},
		{"foreign reservation", func() (int64, error) { return reserve(other, 900) }, 0},
	} {
		if n, e := step.upsert(); e != nil || n != step.rows {
			t.Fatalf("%s: rows=%d err=%v", step.name, n, e)
		}
	}
	var source string
	var generation, cpu int64
	if e := f.st.Pool.QueryRow(ctx, `SELECT r.source_hash,s.controller_generation,h.cpu_millis FROM cross_host_recoveries r JOIN cross_host_recovery_servers s ON s.parent_id=r.parent_id JOIN cross_host_reservations h ON h.parent_id=r.parent_id WHERE r.workspace_id=$1 AND r.parent_id=$2`, ws, parent).Scan(&source, &generation, &cpu); e != nil {
		t.Fatal(e)
	}
	if source != "owned" || generation != 7 || cpu != 100 {
		t.Fatalf("foreign write changed recovery state: source=%q generation=%d cpu=%d", source, generation, cpu)
	}

	checkpoint := uuid.New()
	if e := q.InsertRecoveryBaseCheckpoint(ctx, db.InsertRecoveryBaseCheckpointParams{ID: checkpoint, WorkspaceID: ws, ParentID: parent, Descriptor: []byte(`{}`), Envelope: []byte("sealed")}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.RecoveryCheckpointEnvelope(ctx, db.RecoveryCheckpointEnvelopeParams{WorkspaceID: other, ID: checkpoint}); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatalf("foreign checkpoint read: %v", e)
	}
	for name, write := range map[string]func() error{
		"acknowledge": func() error {
			return q.SetRecoveryCheckpointAcknowledged(ctx, db.SetRecoveryCheckpointAcknowledgedParams{Acknowledged: true, WorkspaceID: other, ID: checkpoint})
		},
		"attach": func() error {
			return q.AttachRecoveryCheckpoint(ctx, db.AttachRecoveryCheckpointParams{Descriptor: []byte(`{"foreign":true}`), WorkspaceID: other, ParentID: parent, ChildIds: []uuid.UUID{parent}})
		},
		"hold": func() error {
			return q.HoldRecoveredOperation(ctx, db.HoldRecoveredOperationParams{Note: "foreign", WorkspaceID: other, ID: parent})
		},
		"frame": func() error {
			return q.SetRecoveryFrameCommand(ctx, db.SetRecoveryFrameCommandParams{Command: []byte(`{"foreign":true}`), WorkspaceID: other, ID: parent})
		},
	} {
		if e := write(); e != nil {
			t.Fatalf("%s: %v", name, e)
		}
	}
	var acknowledged bool
	var state, command string
	if e := f.st.Pool.QueryRow(ctx, `SELECT c.acknowledged,o.state,o.command::text FROM cross_host_checkpoints c JOIN execution_operations o ON o.id=c.parent_id WHERE c.id=$1`, checkpoint).Scan(&acknowledged, &state, &command); e != nil {
		t.Fatal(e)
	}
	if acknowledged || state != "succeeded" || command != "{}" {
		t.Fatalf("foreign write changed recovery state: acknowledged=%v state=%q command=%s", acknowledged, state, command)
	}
}

// PLAT-119: a backup policy's conflict target is its global ID, so the update
// must not replace another workspace's or environment's configuration or access.
func TestBackupPolicyUpsertStaysInWorkspace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, env, parent := seedDiagnosticHistory(t, f)
	var server uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "SELECT server_id FROM execution_operations WHERE id=$1", parent).Scan(&server); e != nil {
		t.Fatal(e)
	}
	id := uuid.New()
	q := db.New(f.st.Pool)
	upsert := func(workspace, environment uuid.UUID, access string) (int64, error) {
		return q.UpsertBackupPolicy(ctx, db.UpsertBackupPolicyParams{ID: id, WorkspaceID: workspace, EnvironmentID: environment, ServerID: server, Configuration: []byte(`{}`), EncryptedAccess: []byte(access)})
	}
	for _, step := range []struct {
		name                   string
		workspace, environment uuid.UUID
		access                 string
		rows                   int64
	}{
		{"insert", ws, env, "owned", 1},
		{"update", ws, env, "rotated", 1},
		{"foreign workspace", uuid.New(), env, "foreign", 0},
		{"foreign environment", ws, uuid.New(), "foreign", 0},
	} {
		if n, e := upsert(step.workspace, step.environment, step.access); e != nil || n != step.rows {
			t.Fatalf("%s: rows=%d err=%v", step.name, n, e)
		}
	}
	stored, e := q.BackupPolicyAccess(ctx, db.BackupPolicyAccessParams{WorkspaceID: ws, EnvironmentID: env, ServerID: server, ID: id})
	if e != nil {
		t.Fatal(e)
	}
	if string(stored.EncryptedAccess) != "rotated" {
		t.Fatalf("foreign upsert replaced access: %q", stored.EncryptedAccess)
	}
}
