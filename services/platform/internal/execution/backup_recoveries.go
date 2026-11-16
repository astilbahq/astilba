package execution

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"slices"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StartBackupRecovery restores one of a revoked server's policy snapshots into a new volume on the
// environment's current primary (docs/backups.md). The policy stays bound to the revoked server and
// is never written again; the restore is recorded in backup_recoveries. Only a snapshot management
// received from the policy's server can be chosen, and the restore takes nothing else from that
// server's reports: the agent reads what it restores out of the snapshot itself.
func (s *Service) StartBackupRecovery(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupRecoveryRequest) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: true}, "backup.recovery.start", id, func(tx pgx.Tx) error {
		if !in.Acknowledged {
			return domain.Invalid("acknowledged")
		}
		if !backup.SnapshotID(in.Snapshot) {
			return domain.Invalid("snapshot")
		}
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if err = requireActive(v.State); err != nil {
			return err
		}
		source, err := loadBackupPolicy(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		previous, err := db.New(tx).OperationCommandByRequestKey(ctx, db.OperationCommandByRequestKeyParams{WorkspaceID: ws, RequestKey: in.RequestKey})
		if err == nil {
			var c api.ExecutionCommand
			var r backup.Recovery
			if json.Unmarshal(previous, &c) != nil || c.Kind != api.ExecutionCommandKindRestore || c.Backup == nil || json.Unmarshal(*c.Backup, &r) != nil || r.Installation.Policy.ID != id || r.Snapshot != in.Snapshot {
				return domain.Conflict
			}
			out, err = operationByRequestKey(ctx, tx, ws, in.RequestKey)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if source.Method == "container-job" {
			return domain.Invalid("policy.method")
		}
		// Only a policy whose server can no longer answer is restored elsewhere. A server that
		// still answers restores its own snapshots, through its own schedule.
		revoked, err := serverRevoked(ctx, tx, ws, source.ServerID)
		if err != nil {
			return err
		}
		if !revoked {
			return domain.Invalid("policy.server")
		}
		if v.Server == nil || v.Applied == nil || *v.Server == source.ServerID {
			return domain.Invalid("deployment")
		}
		server := *v.Server
		if err = s.deploymentServer(ctx, tx, ws, server); err != nil {
			return err
		}
		if err = requireRecoveryRestore(ctx, tx, ws, server); err != nil {
			return err
		}
		if err = noActiveUpgrade(ctx, tx, server); err != nil {
			return err
		}
		raw, err := db.New(tx).BackupDestinationConfiguration(ctx, db.BackupDestinationConfigurationParams{WorkspaceID: ws, ID: source.DestinationID})
		if err != nil {
			return err
		}
		var destination backup.Destination
		if err = json.Unmarshal(raw, &destination); err != nil {
			return err
		}
		// A filesystem destination's repository was on the lost server.
		if destination.Kind != "s3" {
			return domain.Invalid("policy.destination")
		}
		receipts, err := db.New(tx).BackupSnapshotReceipts(ctx, db.BackupSnapshotReceiptsParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return err
		}
		var received backup.Receipts
		if err = json.Unmarshal(receipts, &received); err != nil {
			return err
		}
		reported, ok := received[in.Snapshot]
		if !ok {
			return domain.Invalid("snapshot")
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
		if plan.ServerID != server {
			return domain.Invalid("deployment")
		}
		if err = requireRecoveredResource(source, plan.Specification); err != nil {
			return err
		}
		if err = requireServiceOutputs(ctx, tx, ws, server, plan.Specification); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, server, plan); err != nil {
			return err
		}
		// The agent sees the policy's identity, destination and repository, bound to the server
		// it restores on and the plan applied there now.
		view := source
		view.ServerID, view.Plan = server, plan
		r := backup.Recovery{Installation: backup.Installation{Policy: view, Destination: destination}, RunID: uuid.New(), Snapshot: in.Snapshot, TargetID: uuid.New()}
		if plan.Specification.Compose != nil {
			r.TargetID = composeproject.ResourceID(plan.UnitID, "volume", "restored-"+r.RunID.String())
		}
		if r.Validate() != nil {
			return domain.Invalid("policy")
		}
		generation, err := db.New(tx).AdvanceEnvironmentExecutionGeneration(ctx, db.AdvanceEnvironmentExecutionGenerationParams{WorkspaceID: ws, ID: env})
		if err != nil {
			return err
		}
		payload, _ := stablejson.Marshal(r)
		value := jsontext.Value(payload)
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, Generation: generation, PlanHash: r.Digest(), Kind: api.ExecutionCommandKindRestore, Action: "inspect", ResourceId: id, Deadline: time.Now().Add(r.Lifetime()), Backup: &value}
		if out, err = s.insert(ctx, tx, ws, in.RequestKey, nil, c); err != nil {
			return err
		}
		return db.New(tx).InsertBackupRecovery(ctx, db.InsertBackupRecoveryParams{ID: r.RunID, WorkspaceID: ws, EnvironmentID: env, PolicyID: id, OperationID: out.Id, SourceServerID: source.ServerID, ServerID: server, PlanID: plan.ID, Snapshot: in.Snapshot, FirstReportedAt: reported, TargetVolumeID: r.TargetID, CreatedAt: out.CreatedAt})
	})
	return operationFor(p, out), err
}

// requireRecoveredResource refuses a restore whose resource the receiving deployment no longer
// declares as the policy described it: a volume must still be declared, and a PostgreSQL database
// must still be there with the same database and owner, since the restored cluster is opened by
// the deployment's own credentials.
func requireRecoveredResource(source backup.Policy, spec domain.Specification) error {
	if source.Method == backup.QuiescedFiles {
		if slices.ContainsFunc(spec.Volumes, func(v domain.Volume) bool { return v.ID == source.ResourceID }) {
			return nil
		}
		return domain.Invalid("policy.resource")
	}
	var was *domain.Postgres
	for _, a := range source.Plan.Specification.Applications {
		if a.ID == source.ResourceID {
			was = a.Postgres
		}
	}
	for _, a := range spec.Applications {
		if a.ID == source.ResourceID && a.Postgres != nil && was != nil && a.Postgres.Database == was.Database && a.Postgres.Username == was.Username {
			return nil
		}
	}
	return domain.Invalid("policy.resource")
}

func requireRecoveryRestore(ctx context.Context, q db.DBTX, ws, server uuid.UUID) error {
	raw, err := db.New(q).ServerCapabilities(ctx, db.ServerCapabilitiesParams{WorkspaceID: ws, ID: server})
	if err != nil {
		return err
	}
	// An agent without the restore kind would hold the command unsettled.
	var c api.AgentCapabilities
	if json.Unmarshal(raw, &c) != nil || c.RecoveryRestore == nil || !*c.RecoveryRestore {
		return domain.Invalid("server.capabilities.recoveryRestore")
	}
	return nil
}

// BackupRecoveries lists restores of a policy's snapshots onto another server, newest first.
func (s *Service) BackupRecoveries(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) ([]api.BackupRecoveryRecord, error) {
	out := []api.BackupRecoveryRecord{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		rows, err := db.New(tx).ListBackupRecoveries(ctx, db.ListBackupRecoveriesParams{WorkspaceID: ws, EnvironmentID: env, PolicyID: id})
		if err != nil {
			return err
		}
		for _, row := range rows {
			v := api.BackupRecoveryRecord{Id: row.ID, OperationId: row.OperationID, SourceServerId: row.SourceServerID, ServerId: row.ServerID, PlanId: row.PlanID, Snapshot: row.Snapshot, FirstReportedAt: row.FirstReportedAt.UTC(), TargetId: row.TargetVolumeID, CreatedAt: row.CreatedAt.UTC(), State: api.ExecutionState(row.State), Note: row.Note}
			if row.Result != nil {
				var result struct {
					Manifest api.BackupManifest `json:"manifest"`
				}
				if err = json.Unmarshal(row.Result, &result); err != nil {
					return err
				}
				v.Restored = &struct {
					Manifest api.BackupManifest `json:"manifest"`
				}{result.Manifest}
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// hydrateRecovery fills a restore command's repository access, or with nil keys only makes its
// checks. It is released only to the server the restore was recorded for, as the operation it
// was issued as, while the policy is the generation it froze and its server is still revoked.
func (s *Service) hydrateRecovery(ctx context.Context, keys valueKeys, c *api.ExecutionCommand) error {
	if c.CancelRequested || time.Now().After(c.Deadline) {
		return nil
	}
	var r backup.Recovery
	if c.Backup == nil || json.Unmarshal(*c.Backup, &r) != nil || r.Digest() != c.PlanHash || r.Validate() != nil || r.Installation.Policy.ID != c.ResourceId || r.Installation.Policy.ServerID != c.ServerId {
		return domain.Invalid("backup")
	}
	ws := r.Installation.Policy.WorkspaceID
	if err := requireRecoveryRestore(ctx, s.Store.Pool, ws, c.ServerId); err != nil {
		return err
	}
	stored, err := s.Store.Queries.BackupRecoveryAccess(ctx, db.BackupRecoveryAccessParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId, ID: r.RunID, OperationID: c.Id, ServerID: c.ServerId, PolicyID: c.ResourceId})
	if err != nil {
		return err
	}
	var policy backup.Policy
	if json.Unmarshal(stored.Configuration, &policy) != nil || policy.Generation != r.Installation.Policy.Generation || policy.DestinationID != r.Installation.Destination.ID {
		return domain.Conflict
	}
	// A renewed source server writes to the same repository again, through its own schedule.
	revoked, err := serverRevoked(ctx, s.Store.Pool, ws, policy.ServerID)
	if err != nil {
		return err
	}
	if !revoked {
		return domain.Conflict
	}
	if keys == nil {
		return nil
	}
	if err = openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupPolicy, ID: c.ResourceId}, stored.EncryptedAccess, &r.Installation.Access); err != nil {
		return err
	}
	destination, err := s.Store.Queries.BackupDestinationAccess(ctx, db.BackupDestinationAccessParams{WorkspaceID: ws, ID: policy.DestinationID})
	if err != nil {
		return err
	}
	if err = json.Unmarshal(destination.Configuration, &r.Installation.Destination); err != nil {
		return err
	}
	var a backup.Access
	if err = openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupDestination, ID: policy.DestinationID}, destination.EncryptedAccess, &a); err != nil {
		return err
	}
	r.Installation.Access.AccessKey, r.Installation.Access.SecretKey = a.AccessKey, a.SecretKey
	raw, _ := stablejson.Marshal(r)
	value := jsontext.Value(raw)
	c.Backup = &value
	return nil
}

// recoveryReport takes a restore command's report. Only a succeeded restore carries its result,
// which must be the snapshot and target it was issued for and a manifest the policy admits.
func recoveryReport(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, r *api.AgentReport) error {
	if c.Kind != api.ExecutionCommandKindRestore {
		if r.Restore != nil {
			return domain.Invalid("restore")
		}
		return nil
	}
	if r.State != "succeeded" {
		if r.Restore != nil {
			return domain.Invalid("restore")
		}
		return nil
	}
	var v backup.Recovery
	var result backup.RecoveryResult
	if r.Restore == nil || c.Backup == nil || json.Unmarshal(*c.Backup, &v) != nil || json.Unmarshal(*r.Restore, &result) != nil || result.Snapshot != v.Snapshot || result.TargetID != v.TargetID || v.Admits(result.Manifest) != nil {
		return domain.Invalid("restore")
	}
	raw, err := stablejson.Marshal(result)
	if err != nil {
		return err
	}
	n, err := db.New(tx).RecordBackupRecoveryResult(ctx, db.RecordBackupRecoveryResultParams{WorkspaceID: ws, OperationID: c.Id, ServerID: c.ServerId, Result: raw})
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.Conflict
	}
	return nil
}
