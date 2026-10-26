package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) BackupDestinations(ctx context.Context, p store.Principal, ws uuid.UUID) ([]backup.Destination, error) {
	out := []backup.Destination{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT configuration FROM backup_destinations WHERE workspace_id=$1 ORDER BY id LIMIT 100", ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var v backup.Destination
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Service) CreateBackupDestination(ctx context.Context, p store.Principal, ws uuid.UUID, d backup.Destination, a backup.Access) (backup.Destination, error) {
	d.ID = uuid.New()
	a.Password = ""
	if backup.ValidateDestination(d, a) != nil {
		return d, domain.Invalid("destination")
	}
	err := s.scope(ctx, p, ws, true, true, true, "backup.destination.create", d.ID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM backup_destinations WHERE workspace_id=$1", ws).Scan(&n); err != nil {
			return err
		}
		if n >= 100 {
			return domain.Conflict
		}
		secret, err := s.sealIntegration(ws, d.ID, "backup-destination", a)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(d)
		_, err = tx.Exec(ctx, "INSERT INTO backup_destinations(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4)", d.ID, ws, raw, secret)
		return err
	})
	return d, err
}
func (s *Service) BackupPolicies(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]backup.Policy, error) {
	out := []backup.Policy{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT configuration FROM backup_policies WHERE workspace_id=$1 AND environment_id=$2 ORDER BY id LIMIT 100", ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var v backup.Policy
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Service) SaveBackupPolicy(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupPolicyRequest) (backup.Policy, error) {
	var out backup.Policy
	err := s.scope(ctx, p, ws, true, true, true, "backup.policy.save", id, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Server == nil || v.Applied == nil {
			return domain.Invalid("deployment")
		}
		if err = s.deploymentServer(ctx, tx, ws, *v.Server); err != nil {
			return err
		}
		if err = noActiveUpgrade(ctx, tx, *v.Server); err != nil {
			return err
		}
		var capRaw []byte
		if err = tx.QueryRow(ctx, "SELECT capabilities FROM servers WHERE id=$1", *v.Server).Scan(&capRaw); err != nil {
			return err
		}
		var capabilities api.AgentCapabilities
		if json.Unmarshal(capRaw, &capabilities) != nil || capabilities.Backups == nil || !*capabilities.Backups {
			return domain.Invalid("server.capabilities.backups")
		}
		var active bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))", *v.Server).Scan(&active); err != nil {
			return err
		}
		if active {
			return domain.Conflict
		}

		var planID uuid.UUID
		if err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id IS NOT NULL", ws, env).Scan(&planID); err != nil {
			return err
		}
		plan, err := readPlan(ctx, tx, ws, env, planID)
		if err != nil {
			return err
		}
		if err = requireServiceOutputs(ctx, tx, ws, *v.Server, plan.Specification); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, *v.Server, plan); err != nil {
			return err
		}
		if id == uuid.Nil {
			id = uuid.New()
			if in.BaseGeneration != 0 {
				return domain.Conflict
			}
		} else {
			var raw []byte
			if err = tx.QueryRow(ctx, "SELECT configuration FROM backup_policies WHERE workspace_id=$1 AND environment_id=$2 AND id=$3 FOR UPDATE", ws, env, id).Scan(&raw); err != nil {
				return err
			}
			var old backup.Policy
			if json.Unmarshal(raw, &old) != nil {
				return domain.Conflict
			}
			if old.Generation != in.BaseGeneration || old.ServerID != *v.Server || old.DestinationID != in.DestinationId || old.ResourceID != in.ResourceId || old.Method != string(in.Method) {
				return domain.Conflict
			}
		}
		out = backup.Policy{ID: id, Name: in.Name, WorkspaceID: ws, EnvironmentID: env, ServerID: *v.Server, DestinationID: in.DestinationId, ResourceID: in.ResourceId, Generation: in.BaseGeneration + 1, Method: string(in.Method), Schedule: backup.Schedule{Hour: in.Schedule.Hour, Minute: in.Schedule.Minute, Timezone: in.Schedule.Timezone}, Retain: in.Retain, Enabled: in.Enabled, Command: in.Command, Plan: plan}
		if in.Schedule.Kind != nil && *in.Schedule.Kind != "daily" {
			out.Schedule.Kind = string(*in.Schedule.Kind)
		}
		if in.Schedule.Expression != nil {
			out.Schedule.Expression = *in.Schedule.Expression
		}
		if in.Schedule.IntervalMinutes != nil {
			out.Schedule.IntervalMinutes = *in.Schedule.IntervalMinutes
		}
		if in.TimeoutSeconds != nil {
			out.TimeoutSeconds = *in.TimeoutSeconds
		}
		if out.Validate() != nil {
			return domain.Invalid("policy")
		}
		if out.GeneralSchedule() && (capabilities.Schedules == nil || !*capabilities.Schedules) {
			return domain.Invalid("server.capabilities.schedules")
		}
		if out.Method != "container-job" {
			var exists bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_destinations WHERE workspace_id=$1 AND id=$2)", ws, out.DestinationID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return domain.NotFound
			}
			if in.Password == nil || len(*in.Password) < 16 || len(*in.Password) > 512 {
				return domain.Invalid("password")
			}
		}
		var n int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_policies WHERE workspace_id=$1", ws).Scan(&n); err != nil {
			return err
		}
		if n >= 100 && in.BaseGeneration == 0 {
			return domain.Conflict
		}
		a := backup.Access{}
		if in.Password != nil {
			a.Password = *in.Password
		}
		cipher, err := s.sealIntegration(ws, id, "backup-policy", a)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(out)
		_, err = tx.Exec(ctx, `INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET configuration=excluded.configuration,encrypted_access=excluded.encrypted_access`, id, ws, env, out.ServerID, raw, cipher)
		return err
	})
	return out, err
}
func loadBackupPolicy(ctx context.Context, tx pgx.Tx, ws, env, id uuid.UUID) (backup.Policy, error) {
	var v backup.Policy
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT configuration FROM backup_policies WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	return v, err
}
func (s *Service) BackupCommand(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupCommandRequest) (api.Operation, error) {
	var out api.Operation
	action := "backup.command"
	if in.Action == "restore" || in.Action == "check" || in.Action == "snapshots" {
		action = "backup.recover"
	}
	err := s.scope(ctx, p, ws, true, true, true, action, id, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		v, err := loadBackupPolicy(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		if err = s.deploymentServer(ctx, tx, ws, v.ServerID); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, v.ServerID, v.Plan); err != nil {
			return err
		}
		var previous []byte
		err = tx.QueryRow(ctx, "SELECT command FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, in.RequestKey).Scan(&previous)
		if err == nil {
			var c api.ExecutionCommand
			var b backup.Command
			if json.Unmarshal(previous, &c) != nil || c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil || b.Installation.Policy.ID != id || b.Action != string(in.Action) || b.Snapshot != in.Snapshot {
				return domain.Conflict
			}
			out, err = scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, in.RequestKey))
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		b := backup.Command{Action: string(in.Action), Installation: backup.Installation{Policy: v}, RunID: uuid.New(), Snapshot: in.Snapshot}
		if b.Action == "restore" {
			b.TargetID = uuid.New()
			if v.Plan.Specification.Compose != nil {
				b.TargetID = composeproject.ResourceID(v.Plan.UnitID, "volume", "restored-"+b.RunID.String())
			}
		}
		if v.Method != "container-job" {
			var raw []byte
			if err = tx.QueryRow(ctx, "SELECT configuration FROM backup_destinations WHERE workspace_id=$1 AND id=$2", ws, v.DestinationID).Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &b.Installation.Destination); err != nil {
				return err
			}
		}
		if b.Action == "restore" && !backup.SnapshotID(b.Snapshot) {
			return domain.Invalid("snapshot")
		}
		if err = requireGeneralSchedule(ctx, tx, ws, v.ServerID, v); err != nil {
			return err
		}
		var generation int64
		if err = tx.QueryRow(ctx, "UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation", ws, env).Scan(&generation); err != nil {
			return err
		}
		raw, _ := json.Marshal(b)
		payload := json.RawMessage(raw)
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: v.ServerID, EnvironmentId: env, Generation: generation, PlanHash: b.Digest(), Kind: "backup", Action: "inspect", ResourceId: id, Deadline: time.Now().Add(30 * time.Minute), Backup: &payload}
		if v.GeneralSchedule() {
			c.Kind = "schedule"
		}
		out, err = s.insert(ctx, tx, ws, in.RequestKey, nil, c)
		return err
	})
	return out, err
}
func (s *Service) hydrateBackup(ctx context.Context, c *api.ExecutionCommand) error {
	if c.CancelRequested || time.Now().After(c.Deadline) {
		return nil
	}
	var b backup.Command
	if c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil || b.Digest() != c.PlanHash {
		return domain.Invalid("backup")
	}
	if (c.Kind == "schedule") != b.Installation.Policy.GeneralSchedule() {
		return domain.Invalid("schedule.command")
	}
	if err := requireGeneralSchedule(ctx, s.Store.Pool, b.Installation.Policy.WorkspaceID, c.ServerId, b.Installation.Policy); err != nil {
		return err
	}
	if err := requireServiceOutputs(ctx, s.Store.Pool, b.Installation.Policy.WorkspaceID, c.ServerId, b.Installation.Policy.Plan.Specification); err != nil {
		return err
	}
	if err := requireReplicas(ctx, s.Store.Pool, b.Installation.Policy.WorkspaceID, c.ServerId, b.Installation.Policy.Plan); err != nil {
		return err
	}
	var cipher []byte
	var current []byte
	err := s.Store.Pool.QueryRow(ctx, "SELECT encrypted_access,configuration FROM backup_policies WHERE workspace_id=$1 AND environment_id=$2 AND server_id=$3 AND id=$4", b.Installation.Policy.WorkspaceID, c.EnvironmentId, c.ServerId, c.ResourceId).Scan(&cipher, &current)
	if err != nil {
		return err
	}
	var policy backup.Policy
	if json.Unmarshal(current, &policy) != nil || policy.Generation != b.Installation.Policy.Generation {
		return domain.Conflict
	}
	if err = s.openIntegration(b.Installation.Policy.WorkspaceID, c.ResourceId, "backup-policy", cipher, &b.Installation.Access); err != nil {
		return err
	}
	if b.Installation.Policy.Method != "container-job" {
		var raw []byte
		err = s.Store.Pool.QueryRow(ctx, "SELECT configuration,encrypted_access FROM backup_destinations WHERE workspace_id=$1 AND id=$2", b.Installation.Policy.WorkspaceID, b.Installation.Policy.DestinationID).Scan(&raw, &cipher)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &b.Installation.Destination); err != nil {
			return err
		}
		var a backup.Access
		if err = s.openIntegration(b.Installation.Policy.WorkspaceID, b.Installation.Policy.DestinationID, "backup-destination", cipher, &a); err != nil {
			return err
		}
		b.Installation.Access.AccessKey = a.AccessKey
		b.Installation.Access.SecretKey = a.SecretKey
	}
	raw, _ := json.Marshal(b)
	value := json.RawMessage(raw)
	c.Backup = &value
	return nil
}
func (s *Service) BackupStatus(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) (backup.Status, error) {
	out := backup.Status{PolicyID: id, Runs: []backup.Run{}}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT status FROM backup_policies WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out)
	})
	return out, err
}
func (s *Service) ScheduleReport(ctx context.Context, a Identity, sequence int64, status backup.Status) error {
	if a.Role == "build" || sequence < 1 || sequence > 1<<53 || len(status.Runs) > 128 {
		return domain.Invalid("scheduleReport")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	if err = tx.QueryRow(ctx, "SELECT NOT revoked AND (credential_hash=$2 OR (previous_hash=$2 AND previous_until>now())) FROM servers WHERE id=$1 FOR UPDATE", a.ID, hash(a.Token)).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return domain.Unauthenticated
	}
	var raw []byte
	var seq int64
	if err = tx.QueryRow(ctx, "SELECT configuration,status_sequence FROM backup_policies WHERE workspace_id=$1 AND id=$2 AND server_id=$3 FOR UPDATE", a.Workspace, status.PolicyID, a.ID).Scan(&raw, &seq); err != nil {
		return err
	}
	var p backup.Policy
	if json.Unmarshal(raw, &p) != nil || status.Generation > p.Generation {
		return domain.Conflict
	}
	if sequence <= seq {
		return tx.Commit(ctx)
	}
	for _, r := range status.Runs {
		if r.PolicyID != p.ID || r.Generation < 1 || r.Generation > p.Generation || r.ID == uuid.Nil || len(r.Note) > 1024 || r.Snapshot != "" && !backup.SnapshotID(r.Snapshot) || r.Manifest != nil && (r.Manifest.PolicyID != p.ID || r.Manifest.EnvironmentID != p.EnvironmentID || r.Manifest.ResourceID != p.ResourceID) {
			return domain.Invalid("run")
		}
	}
	raw, _ = json.Marshal(status)
	_, err = tx.Exec(ctx, "UPDATE backup_policies SET status=$2,status_sequence=$3 WHERE id=$1", p.ID, raw, sequence)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) PrepareBackupCutover(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupCutoverRequest) (db.SpecificationRevision, error) {
	var out db.SpecificationRevision
	err := s.scope(ctx, p, ws, true, true, true, "backup.cutover.prepare", id, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Revision != in.BaseRevisionId {
			return domain.Conflict
		}
		policy, err := loadBackupPolicy(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT status FROM backup_policies WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id).Scan(&raw); err != nil {
			return err
		}
		var status backup.Status
		if json.Unmarshal(raw, &status) != nil {
			return domain.Conflict
		}
		var run backup.Run
		for _, r := range status.Runs {
			if r.ID == in.RunId {
				run = r
			}
		}
		if !run.Verified || run.TargetID == uuid.Nil || run.State != "succeeded" || run.Manifest == nil || v.Applied == nil || policy.Plan.RevisionID != *v.Applied {
			return domain.Conflict
		}
		source := policy.ResourceID
		if policy.Method == "postgres-logical" {
			for _, a := range policy.Plan.Specification.Applications {
				if a.ID == source && a.Postgres != nil {
					source = a.Mounts[0].VolumeID
				}
			}
		}
		if v.Spec.Compose != nil {
			oldName := ""
			for _, volume := range v.Spec.Volumes {
				if volume.ID == source {
					oldName = volume.Name
				}
			}
			next := "restored-" + run.ID.String()
			if oldName == "" || run.TargetID != composeproject.ResourceID(v.Unit, "volume", next) {
				return domain.Conflict
			}
			bundle, err := composeproject.RenameVolume(ctx, *v.Spec.Compose, domain.VariableValues(v.Spec.Variables, nil), oldName, next)
			if err != nil {
				return domain.Invalid("compose.restoreVolume")
			}
			v.Spec.Compose = &bundle
		} else {
			found := false
			for i := range v.Spec.Volumes {
				if v.Spec.Volumes[i].ID == source {
					v.Spec.Volumes[i].ID = run.TargetID
					found = true
				}
			}
			if !found {
				return domain.Conflict
			}
			for i := range v.Spec.Applications {
				for j := range v.Spec.Applications[i].Mounts {
					if v.Spec.Applications[i].Mounts[j].VolumeID == source {
						v.Spec.Applications[i].Mounts[j].VolumeID = run.TargetID
					}
				}
			}
		}

		raw, _ = json.Marshal(v.Spec)
		_, canonical, err := domain.ParseSpec(raw)
		if err != nil {
			return err
		}
		h := sha256.Sum256(canonical)
		q := db.New(tx)
		out, err = q.InsertRevision(ctx, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: uuid.New(), AuthorID: p.Account.ID, Specification: canonical, ContentHash: h[:]})
		if err != nil {
			return err
		}
		n, err := tx.Exec(ctx, "UPDATE environments SET draft_revision_id=$4 WHERE workspace_id=$1 AND id=$2 AND draft_revision_id=$3", ws, env, in.BaseRevisionId, out.ID)
		if err == nil && n.RowsAffected() != 1 {
			return domain.Conflict
		}
		return err
	})
	return out, err
}

func requireGeneralSchedule(ctx context.Context, q releaseQuery, ws, server uuid.UUID, p backup.Policy) error {
	if !p.GeneralSchedule() {
		return nil
	}
	var raw []byte
	if err := q.QueryRow(ctx, "SELECT capabilities FROM servers WHERE workspace_id=$1 AND id=$2", ws, server).Scan(&raw); err != nil {
		return err
	}
	var cap api.AgentCapabilities
	if json.Unmarshal(raw, &cap) != nil || cap.Schedules == nil || !*cap.Schedules {
		return domain.Invalid("server.capabilities.schedules")
	}
	return nil
}
