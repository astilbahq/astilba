package execution

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deadline"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) BackupDestinations(ctx context.Context, p store.Principal, ws uuid.UUID) ([]api.BackupDestination, error) {
	out := []api.BackupDestination{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := db.New(tx).ListBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		exposed, err := db.New(tx).ExposedBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		policies, err := db.New(tx).BackupDestinationPolicies(ctx, ws)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var v backup.Destination
			if err = json.Unmarshal(row.Configuration, &v); err != nil {
				return err
			}
			view := destinationView(v, row.ServerID, row.Shared, slices.Contains(exposed, v.ID))
			for _, p := range policies {
				if p.DestinationID == v.ID {
					view.Policies = append(view.Policies, api.BackupDestinationPolicy{Id: p.ID, Name: p.Name, ProjectId: p.ProjectID, EnvironmentId: p.EnvironmentID, EnvironmentName: p.EnvironmentName})
				}
			}
			out = append(out, view)
		}
		return nil
	})
	return out, err
}

// destinationView is d, bound to server or shared, as the API shows it.
func destinationView(d backup.Destination, server *uuid.UUID, shared, exposed bool) api.BackupDestination {
	v := api.BackupDestination{Id: d.ID, Name: d.Name, Kind: api.BackupDestinationKind(d.Kind), Root: d.Root, Endpoint: d.Endpoint, Bucket: d.Bucket, Region: d.Region, ServerId: server, Shared: shared, Exposed: exposed, Policies: []api.BackupDestinationPolicy{}}
	if d.Prefix != "" {
		v.Prefix = &d.Prefix
	}
	return v
}

// CreateBackupDestination adds destination d with access a. An S3 destination is bound to server,
// the deployment server its keys are given to, and a filesystem destination has none.
func (s *Service) CreateBackupDestination(ctx context.Context, p store.Principal, ws uuid.UUID, d backup.Destination, a backup.Access, server *uuid.UUID) (api.BackupDestination, error) {
	d.ID = uuid.New()
	a.Password = ""
	if d.Kind == "s3" && d.Prefix != "" && !backup.ValidPrefix(d.Prefix) {
		return api.BackupDestination{}, domain.Invalid("prefix")
	}
	if backup.ValidateDestination(d, a) != nil {
		return api.BackupDestination{}, domain.Invalid("destination")
	}
	if (d.Kind == "s3") != (server != nil) {
		return api.BackupDestination{}, domain.Invalid("serverId")
	}
	item := secretbroker.Item{Kind: secretbroker.BackupDestination, ID: d.ID}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return api.BackupDestination{}, err
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
		if server != nil {
			if err = s.admitDestination(ctx, tx, ws, *server, d); err != nil {
				return err
			}
		}
		secret, err := sealIntegration(ctx, db.New(tx), keys, item, a)
		if err != nil {
			return err
		}
		sealed = true
		raw, _ := stablejson.Marshal(d)
		return db.New(tx).InsertBackupDestination(ctx, db.InsertBackupDestinationParams{ID: d.ID, WorkspaceID: ws, Configuration: raw, EncryptedAccess: secret, ServerID: server})
	})
	if err != nil {
		return api.BackupDestination{}, err
	}
	// No policy uses a new destination yet.
	return destinationView(d, server, false, false), nil
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
		if err = requireActive(v.State); err != nil {
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
		if err = requireAgentSupport(ctx, tx, ws, *v.Server, plan.Specification); err != nil {
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
			if err = bindPolicyDestination(ctx, tx, ws, out, creating); err != nil {
				return err
			}
			// A revoked server that held the destination's keys can still delete or overwrite what a
			// new policy writes there, so choosing it is acknowledged and recorded.
			if creating {
				exposed, err := db.New(tx).ExposedBackupDestinations(ctx, ws)
				if err != nil {
					return err
				}
				if slices.Contains(exposed, out.DestinationID) {
					if in.ExposureAcknowledged == nil || !*in.ExposureAcknowledged {
						return domain.Invalid("destination.exposed")
					}
					if err = store.Audit(ctx, db.New(tx), p, ws, "backup.destination.exposure.acknowledge", out.DestinationID); err != nil {
						return err
					}
				}
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
		cipher, err := sealIntegration(ctx, db.New(tx), keys, item, a)
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
		var err error
		out, err = s.backupCommand(ctx, tx, ws, env, id, in)
		return err
	})
	return operationFor(p, out), err
}

// backupCommand queues a backup policy's command in tx, which holds the workspace's turn. An
// owner's request and management's own install after a re-mint both start here.
func (s *Service) backupCommand(ctx context.Context, tx pgx.Tx, ws, env, id uuid.UUID, in api.BackupCommandRequest) (api.Operation, error) {
	var out api.Operation
	err := func() error {
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
			out, err = operationByRequestKey(ctx, tx, ws, in.RequestKey)
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
			if err = requireBackupPrefixes(ctx, tx, ws, v.ServerID, b.Installation.Destination); err != nil {
				return err
			}
		}
		if b.Action == "restore" {
			if err = checkRestoreRequest(ctx, tx, ws, env, v, b.Snapshot); err != nil {
				return err
			}
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
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: v.ServerID, EnvironmentId: env, Generation: generation, PlanHash: b.Digest(), Kind: "backup", Action: "inspect", ResourceId: id, Deadline: time.Now().Add(b.Lifetime()), Backup: &payload}
		if v.GeneralSchedule() {
			c.Kind = "schedule"
		}
		if b.Action == "backup" && v.Plan.Specification.HasRemotePlacement() && v.RequiresQuiescence() {
			out, err = s.insertBackupMaintenance(ctx, tx, ws, in.RequestKey, c, b)
			return err
		}
		out, err = s.insert(ctx, tx, ws, in.RequestKey, nil, c)
		return err
	}()
	return out, err
}

// hydrateBackup fills a backup command's access, or with nil keys only makes its checks.
func (s *Service) hydrateBackup(ctx context.Context, keys valueKeys, c *api.ExecutionCommand) error {
	// Poll sends a backup past its deadline without values. An upgrade's check reaches here with the
	// upgrade's own stop and deadline.
	if c.CancelRequested || deadline.Passed(*c) {
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
	if err := requireAgentSupport(ctx, s.Store.Pool, b.Installation.Policy.WorkspaceID, c.ServerId, b.Installation.Policy.Plan.Specification); err != nil {
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
	if a.Role == "build" || sequence < 1 || sequence > 1<<53 || len(status.Runs) > backup.MaxReportedRuns {
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
		if r.PolicyID != p.ID || r.Generation < 1 || r.Generation > p.Generation || r.ID == uuid.Nil || len(r.Note) > 1024 || r.Snapshot != "" && !backup.SnapshotID(r.Snapshot) || r.Manifest != nil && (r.Manifest.PolicyID != p.ID || r.Manifest.EnvironmentID != p.EnvironmentID || r.Manifest.ResourceID != p.ResourceID) || unknownRestoreImage(r.RestoreImage) || unknownRestoreRoles(r.RestoreRoles) {
			return domain.Invalid("run")
		}
	}
	raw, _ := stablejson.Marshal(status)
	if err = db.New(tx).SaveBackupPolicyReport(ctx, db.SaveBackupPolicyReportParams{Status: raw, StatusSequence: sequence, WorkspaceID: a.Workspace, ID: p.ID}); err != nil {
		return err
	}
	if err = recordSnapshotReceipts(ctx, db.New(tx), a, p.ID, status.Runs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// recordSnapshotReceipts keeps a receipt for each snapshot a report lists as available
// (backup.Available). A snapshot keeps the time management first received it for as long as
// reports go on listing it, whether by its upload or by a restore that verified it after the
// upload left the history. Only an upload adds one, so one never reported stays on the manual
// route.
func recordSnapshotReceipts(ctx context.Context, q *db.Queries, a Identity, policy uuid.UUID, runs []backup.Run) error {
	listed, uploaded := backup.Available(runs)
	return q.RecordBackupSnapshotReceipts(ctx, db.RecordBackupSnapshotReceiptsParams{Listed: listed, Uploaded: uploaded, WorkspaceID: a.Workspace, PolicyID: policy})
}

// BackupSnapshotReceipts lists when management first received each snapshot its policy's reports
// list as available, by its own clock, newest first. Unlike the times a server reports, a host
// can't backdate these.
func (s *Service) BackupSnapshotReceipts(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) ([]api.BackupSnapshotReceipt, error) {
	out := []api.BackupSnapshotReceipt{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		raw, err := db.New(tx).BackupSnapshotReceipts(ctx, db.BackupSnapshotReceiptsParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return err
		}
		var receipts backup.Receipts
		if err = json.Unmarshal(raw, &receipts); err != nil {
			return err
		}
		for snapshot, at := range receipts {
			out = append(out, api.BackupSnapshotReceipt{Snapshot: snapshot, FirstReportedAt: at.UTC()})
		}
		slices.SortFunc(out, func(a, b api.BackupSnapshotReceipt) int {
			return cmp.Or(b.FirstReportedAt.Compare(a.FirstReportedAt), strings.Compare(a.Snapshot, b.Snapshot))
		})
		return nil
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
