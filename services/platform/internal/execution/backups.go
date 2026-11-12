package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) BackupDestinations(ctx context.Context, p store.Principal, ws uuid.UUID) ([]backup.Destination, error) {
	out := []backup.Destination{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := db.New(tx).ListBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		for _, raw := range rows {
			var v backup.Destination
			if err = json.Unmarshal(raw, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}
func (s *Service) CreateBackupDestination(ctx context.Context, p store.Principal, ws uuid.UUID, d backup.Destination, a backup.Access) (backup.Destination, error) {
	d.ID = uuid.New()
	a.Password = ""
	if backup.ValidateDestination(d, a) != nil {
		return d, domain.Invalid("destination")
	}
	item := secretbroker.Item{Kind: secretbroker.BackupDestination, ID: d.ID}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return d, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "backup.destination.create", d.ID, func(tx pgx.Tx) error {
		n, err := db.New(tx).CountBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		if n >= 100 {
			return domain.Conflict
		}
		secret, err := sealIntegration(keys, item, a)
		if err != nil {
			return err
		}
		sealed = true
		raw, _ := stablejson.Marshal(d)
		return db.New(tx).InsertBackupDestination(ctx, db.InsertBackupDestinationParams{ID: d.ID, WorkspaceID: ws, Configuration: raw, EncryptedAccess: secret})
	})
	return d, err
}
func (s *Service) BackupPolicies(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]backup.Policy, error) {
	out := []backup.Policy{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		rows, err := db.New(tx).ListBackupPolicies(ctx, db.ListBackupPoliciesParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		for _, raw := range rows {
			var v backup.Policy
			if err = json.Unmarshal(raw, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}
func (s *Service) SaveBackupPolicy(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupPolicyRequest) (backup.Policy, error) {
	var out backup.Policy
	// A new policy's ID is chosen before its keys are asked for, so its access is sealed for it.
	creating := id == uuid.Nil
	if creating {
		id = uuid.New()
	}
	item := secretbroker.Item{Kind: secretbroker.BackupPolicy, ID: id}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return out, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "backup.policy.save", id, func(tx pgx.Tx) error {
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
		capRaw, err := db.New(tx).ServerCapabilities(ctx, db.ServerCapabilitiesParams{WorkspaceID: ws, ID: *v.Server})
		if err != nil {
			return err
		}
		var capabilities api.AgentCapabilities
		if json.Unmarshal(capRaw, &capabilities) != nil || capabilities.Backups == nil || !*capabilities.Backups {
			return domain.Invalid("server.capabilities.backups")
		}
		active, err := db.New(tx).ServerHasActiveOperations(ctx, db.ServerHasActiveOperationsParams{WorkspaceID: ws, ServerID: *v.Server})
		if err != nil {
			return err
		}
		if active {
			return domain.Conflict
		}

		planID, err := db.New(tx).EnvironmentRuntimePlan(ctx, db.EnvironmentRuntimePlanParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		plan, err := readPlan(ctx, tx, ws, env, planID)
		if err != nil {
			return err
		}
		plan = plan.PrimaryPlan()
		if err = requireServiceOutputs(ctx, tx, ws, *v.Server, plan.Specification); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, *v.Server, plan); err != nil {
			return err
		}
		if creating {
			if in.BaseGeneration != 0 {
				return domain.Conflict
			}
		} else {
			reserved, err := db.New(tx).BackupPolicyMaintenanceReserved(ctx, db.BackupPolicyMaintenanceReservedParams{WorkspaceID: ws, EnvironmentID: env, PolicyID: id.String()})
			if err != nil {
				return err
			}
			if reserved {
				return domain.Conflict
			}
			raw, err := db.New(tx).LockBackupPolicyConfiguration(ctx, db.LockBackupPolicyConfigurationParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
			if err != nil {
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
		out = backup.Policy{Coordinated: plan.Specification.HasRemotePlacement() && string(in.Method) == backup.QuiescedFiles, ID: id, Name: in.Name, WorkspaceID: ws, EnvironmentID: env, ServerID: *v.Server, DestinationID: in.DestinationId, ResourceID: in.ResourceId, Generation: in.BaseGeneration + 1, Method: string(in.Method), Schedule: backup.Schedule{Hour: in.Schedule.Hour, Minute: in.Schedule.Minute, Timezone: in.Schedule.Timezone}, Retain: in.Retain, Enabled: in.Enabled, Command: in.Command, Plan: plan}
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
		if err = refuseElevatedJob(out); err != nil {
			return err
		}
		if out.GeneralSchedule() && (capabilities.Schedules == nil || !*capabilities.Schedules) {
			return domain.Invalid("server.capabilities.schedules")
		}
		if out.Method != "container-job" {
			exists, err := db.New(tx).BackupDestinationExists(ctx, db.BackupDestinationExistsParams{WorkspaceID: ws, ID: out.DestinationID})
			if err != nil {
				return err
			}
			if !exists {
				return domain.NotFound
			}
			if in.Password == nil || len(*in.Password) < 16 || len(*in.Password) > 512 {
				return domain.Invalid("password")
			}
		}
		n, err := db.New(tx).CountBackupPolicies(ctx, ws)
		if err != nil {
			return err
		}
		if n >= 100 && in.BaseGeneration == 0 {
			return domain.Conflict
		}
		a := backup.Access{}
		if in.Password != nil {
			a.Password = *in.Password
		}
		cipher, err := sealIntegration(keys, item, a)
		if err != nil {
			return err
		}
		sealed = true
		raw, _ := stablejson.Marshal(out)
		_, err = db.New(tx).UpsertBackupPolicy(ctx, db.UpsertBackupPolicyParams{ID: id, WorkspaceID: ws, EnvironmentID: env, ServerID: out.ServerID, Configuration: raw, EncryptedAccess: cipher})
		return err
	})
	return out, err
}
func loadBackupPolicy(ctx context.Context, tx pgx.Tx, ws, env, id uuid.UUID) (backup.Policy, error) {
	var v backup.Policy
	raw, err := db.New(tx).BackupPolicyConfiguration(ctx, db.BackupPolicyConfigurationParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
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
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: in.Action == "restore" || in.Action == "check" || in.Action == "snapshots"}, action, id, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		v, err := loadBackupPolicy(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		if err = refuseElevatedJob(v); err != nil {
			return err
		}
		if err = s.deploymentServer(ctx, tx, ws, v.ServerID); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, v.ServerID, v.Plan); err != nil {
			return err
		}
		previous, err := db.New(tx).OperationCommandByRequestKey(ctx, db.OperationCommandByRequestKeyParams{WorkspaceID: ws, RequestKey: in.RequestKey})
		if err == nil {
			var c api.ExecutionCommand
			var b backup.Command
			if json.Unmarshal(previous, &c) != nil {
				return domain.Conflict
			}
			if c.Kind == "crosshost" && c.Crosshost != nil {
				var parent coordinationCommand
				if json.Unmarshal(*c.Crosshost, &parent) != nil || parent.Maintenance == nil {
					return domain.Conflict
				}
				c = parent.Maintenance.Effect.command("")
			}
			if c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil || b.Installation.Policy.ID != id || b.Action != string(in.Action) || b.Snapshot != in.Snapshot {
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
			raw, err := db.New(tx).BackupDestinationConfiguration(ctx, db.BackupDestinationConfigurationParams{WorkspaceID: ws, ID: v.DestinationID})
			if err != nil {
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
		generation, err := db.New(tx).AdvanceEnvironmentExecutionGeneration(ctx, db.AdvanceEnvironmentExecutionGenerationParams{WorkspaceID: ws, ID: env})
		if err != nil {
			return err
		}
		raw, _ := stablejson.Marshal(b)
		payload := jsontext.Value(raw)
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: v.ServerID, EnvironmentId: env, Generation: generation, PlanHash: b.Digest(), Kind: "backup", Action: "inspect", ResourceId: id, Deadline: time.Now().Add(30 * time.Minute), Backup: &payload}
		if v.GeneralSchedule() {
			c.Kind = "schedule"
		}
		if b.Action == "backup" && v.Plan.Specification.HasRemotePlacement() && v.RequiresQuiescence() {
			out, err = s.insertBackupMaintenance(ctx, tx, ws, in.RequestKey, c, b)
			return err
		}
		out, err = s.insert(ctx, tx, ws, in.RequestKey, nil, c)
		return err
	})
	return out, err
}

// hydrateBackup fills a backup command's access, or with nil keys only makes its checks.
func (s *Service) hydrateBackup(ctx context.Context, keys valueKeys, c *api.ExecutionCommand) error {
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
	stored, err := s.Store.Queries.BackupPolicyAccess(ctx, db.BackupPolicyAccessParams{WorkspaceID: b.Installation.Policy.WorkspaceID, EnvironmentID: c.EnvironmentId, ServerID: c.ServerId, ID: c.ResourceId})
	if err != nil {
		return err
	}
	var policy backup.Policy
	if json.Unmarshal(stored.Configuration, &policy) != nil || policy.Generation != b.Installation.Policy.Generation {
		return domain.Conflict
	}
	if keys == nil {
		return nil
	}
	if err = openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupPolicy, ID: c.ResourceId}, stored.EncryptedAccess, &b.Installation.Access); err != nil {
		return err
	}
	if b.Installation.Policy.Method != "container-job" {
		destination, err := s.Store.Queries.BackupDestinationAccess(ctx, db.BackupDestinationAccessParams{WorkspaceID: b.Installation.Policy.WorkspaceID, ID: b.Installation.Policy.DestinationID})
		if err != nil {
			return err
		}
		if err = json.Unmarshal(destination.Configuration, &b.Installation.Destination); err != nil {
			return err
		}
		var a backup.Access
		if err = openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupDestination, ID: b.Installation.Policy.DestinationID}, destination.EncryptedAccess, &a); err != nil {
			return err
		}
		b.Installation.Access.AccessKey = a.AccessKey
		b.Installation.Access.SecretKey = a.SecretKey
	}
	raw, _ := stablejson.Marshal(b)
	value := jsontext.Value(raw)
	c.Backup = &value
	return nil
}
func (s *Service) BackupStatus(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) (backup.Status, error) {
	out := backup.Status{PolicyID: id, Runs: []backup.Run{}}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		raw, err := db.New(tx).BackupPolicyStatus(ctx, db.BackupPolicyStatusParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
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
	valid, err := lockAgentCredential(ctx, tx, a)
	if err != nil {
		return err
	}
	if !valid {
		return domain.Unauthenticated
	}
	report, err := db.New(tx).LockBackupPolicyReport(ctx, db.LockBackupPolicyReportParams{WorkspaceID: a.Workspace, ID: status.PolicyID, ServerID: a.ID})
	if err != nil {
		return err
	}
	var p backup.Policy
	if json.Unmarshal(report.Configuration, &p) != nil || status.Generation > p.Generation {
		return domain.Conflict
	}
	if sequence <= report.StatusSequence {
		return tx.Commit(ctx)
	}
	if p.Coordinated {
		if status.Next.Year() < 1 || status.Next.Year() > 9999 {
			return domain.Invalid("schedule.next")
		}
		status.Next = status.Next.UTC()
		var old backup.Status
		if json.Unmarshal(report.Status, &old) != nil {
			return domain.Conflict
		}
		// Management advances this cursor atomically with the coordinated parent.
		// An unchanged installation report must not rewind it to its local cursor.
		if old.Generation == status.Generation && !old.Next.IsZero() {
			status.Next = old.Next
		}
	}
	for _, r := range status.Runs {
		if r.PolicyID != p.ID || r.Generation < 1 || r.Generation > p.Generation || r.ID == uuid.Nil || len(r.Note) > 1024 || r.Snapshot != "" && !backup.SnapshotID(r.Snapshot) || r.Manifest != nil && (r.Manifest.PolicyID != p.ID || r.Manifest.EnvironmentID != p.EnvironmentID || r.Manifest.ResourceID != p.ResourceID) {
			return domain.Invalid("run")
		}
	}
	raw, _ := stablejson.Marshal(status)
	if err = db.New(tx).SaveBackupPolicyReport(ctx, db.SaveBackupPolicyReportParams{Status: raw, StatusSequence: sequence, WorkspaceID: a.Workspace, ID: p.ID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) PrepareBackupCutover(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupCutoverRequest) (db.SpecificationRevision, error) {
	var out db.SpecificationRevision
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "backup.cutover.prepare", id, func(tx pgx.Tx) error {
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
		raw, err := db.New(tx).BackupPolicyStatus(ctx, db.BackupPolicyStatusParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
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
		var boundary *deployment.RestoreCutover
		if policy.Plan.Local != nil {
			current, err := appliedVisualPlan(ctx, tx, ws, env)
			if err != nil {
				return err
			}
			if current.Distributed == nil || current.PrimaryPlan().Hash != policy.Plan.Hash || deployment.Hash(v.Spec) != deployment.Hash(current.Specification) {
				return domain.Conflict
			}
			source, err = backup.CutoverVolume(policy, run)
			if err != nil {
				return domain.Invalid("restore.compatibility")
			}
			boundary = &deployment.RestoreCutover{SourcePlanID: current.ID, SourceHash: current.Hash, PolicyID: policy.ID, RunID: run.ID, ManifestHash: run.ManifestHash, OldVolumeID: source, TargetVolumeID: run.TargetID}
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

		raw, _ = stablejson.Marshal(v.Spec)
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
		if boundary != nil {
			configuration, _ := stablejson.Marshal(boundary)
			if err = q.InsertBackupCutover(ctx, db.InsertBackupCutoverParams{WorkspaceID: ws, EnvironmentID: env, RevisionID: out.ID, PolicyID: id, SourcePlanID: boundary.SourcePlanID, Configuration: configuration}); err != nil {
				return err
			}
		}
		_, err = q.AdvanceDraft(ctx, db.AdvanceDraftParams{WorkspaceID: ws, ID: env, DraftRevisionID: in.BaseRevisionId, DraftRevisionID_2: out.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Conflict
		}
		return err
	})
	return out, err
}

func requireGeneralSchedule(ctx context.Context, q db.DBTX, ws, server uuid.UUID, p backup.Policy) error {
	if !p.GeneralSchedule() {
		return nil
	}
	raw, err := db.New(q).ServerCapabilities(ctx, db.ServerCapabilitiesParams{WorkspaceID: ws, ID: server})
	if err != nil {
		return err
	}
	var cap api.AgentCapabilities
	if json.Unmarshal(raw, &cap) != nil || cap.Schedules == nil || !*cap.Schedules {
		return domain.Invalid("server.capabilities.schedules")
	}
	return nil
}

// errElevatedJob refuses a scheduled job in a service that review treats as elevated.
var errElevatedJob = &domain.Error{Code: "invalid_input", Message: "This service is privileged or shares a volume or network with a privileged service, so a job's command there could control the server. Scheduled jobs can't run in it.", Fields: []string{"resourceId"}}

// refuseElevatedJob refuses a scheduled job whose source service is privileged or a peer of
// one, see ElevationOf: its command could drive the privileged service and so reach the
// host. The agent refuses it again before every run.
func refuseElevatedJob(p backup.Policy) error {
	if p.Method != "container-job" {
		return nil
	}
	elevation, err := ElevationOf(p.Plan.Specification)
	if err != nil {
		return err
	}
	if elevation.Services[p.ResourceID] {
		return errElevatedJob
	}
	return nil
}
