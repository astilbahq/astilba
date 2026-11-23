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
		if err = requireAgentSupport(ctx, tx, ws, server, plan.Specification); err != nil {
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
		return db.New(tx).InsertBackupRecovery(ctx, db.InsertBackupRecoveryParams{ID: r.RunID, WorkspaceID: ws, EnvironmentID: env, PolicyID: id, OperationID: out.Id, SourceServerID: source.ServerID, ServerID: server, PlanID: planID, Snapshot: in.Snapshot, FirstReportedAt: reported, TargetVolumeID: r.TargetID, CreatedAt: out.CreatedAt})
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

// requireRecoveryRestore admits a restore onto a server whose agent can run one now.
func requireRecoveryRestore(ctx context.Context, q db.DBTX, ws, server uuid.UUID) error {
	able, err := recoveryRestore(ctx, q, ws, server)
	if err == nil && (able == nil || !*able) {
		err = domain.Invalid("server.capabilities.recoveryRestore")
	}
	return err
}

// recoveryRestore reads what a server's agent last advertised for the restore kind: nil from one
// that doesn't run it, which would hold the command unsettled, and otherwise whether its restore
// prerequisites passed at that heartbeat.
func recoveryRestore(ctx context.Context, q db.DBTX, ws, server uuid.UUID) (*bool, error) {
	raw, err := db.New(q).ServerCapabilities(ctx, db.ServerCapabilitiesParams{WorkspaceID: ws, ID: server})
	if err != nil {
		return nil, err
	}
	var c api.AgentCapabilities
	if json.Unmarshal(raw, &c) != nil {
		return nil, nil
	}
	return c.RecoveryRestore, nil
}

// BackupRecoveries lists restores of a policy's snapshots onto another server, newest first.
func (s *Service) BackupRecoveries(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) ([]api.BackupRecoveryRecord, error) {
	out := []api.BackupRecoveryRecord{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		if _, err := loadBackupPolicy(ctx, tx, ws, env, id); err != nil {
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
			v.TargetDeleted = row.TargetDeleted
			if row.DiscardOperationID != nil {
				v.Discard = &struct {
					Note        string             `json:"note"`
					OperationId uuid.UUID          `json:"operationId"`
					State       api.ExecutionState `json:"state"`
				}{row.DiscardNote.String, *row.DiscardOperationID, api.ExecutionState(row.DiscardState.String)}
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// PrepareRecoveryCutover writes a draft that switches a succeeded restore's target in for the
// volume the deployment's resource uses now, as PrepareBackupCutover does for a policy's own
// restore. The restore's recorded result is checked again, and the deployment must still be the
// single-server plan the restore was issued against, on the server it ran on.
func (s *Service) PrepareRecoveryCutover(ctx context.Context, p store.Principal, ws, pid, env, id, recovery uuid.UUID, in api.BackupRecoveryCutoverRequest) (db.SpecificationRevision, error) {
	var out db.SpecificationRevision
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "backup.recovery.cutover", recovery, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if err = requireActive(v.State); err != nil {
			return err
		}
		if v.Revision != in.BaseRevisionId {
			return domain.Conflict
		}
		source, row, err := loadBackupRecovery(ctx, tx, ws, env, id, recovery)
		if err != nil {
			return err
		}
		// Any delete of the volume, a discard or another, claims it until the delete ends.
		deletions, err := store.VolumeDeletions(ctx, db.New(tx), ws, env, []uuid.UUID{row.TargetVolumeID})
		if err != nil {
			return err
		}
		switch deletions[row.TargetVolumeID] {
		case store.VolumeDeleting:
			return domain.DiscardActive
		case store.VolumeDeleted:
			return domain.VolumeDeleted
		}
		if row.State != string(api.ExecutionStateSucceeded) {
			return domain.Conflict
		}
		result, err := backup.ParseRecoveryResult(row.Result, id, env, row.Snapshot, row.TargetVolumeID)
		if err != nil {
			return domain.Conflict
		}
		planID, err := db.New(tx).EnvironmentRuntimePlan(ctx, db.EnvironmentRuntimePlanParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		if v.Server == nil || *v.Server != row.ServerID || planID != row.PlanID {
			return domain.Conflict
		}
		plan, err := readPlan(ctx, tx, ws, env, planID)
		if err != nil {
			return err
		}
		// A distributed deployment switches volumes through its own boundary, which a restore
		// onto another server doesn't record.
		if plan.Distributed != nil {
			return domain.Invalid("deployment.distributed")
		}
		view := source
		view.ServerID, view.Plan = row.ServerID, plan
		old, err := backup.RestoredVolume(view, row.TargetVolumeID, result.Manifest, result.RestoredImage())
		if err != nil {
			return restoreIncompatible(err)
		}
		out, err = writeRestoreDraft(ctx, tx, p, ws, env, v, view, recovery, row.TargetVolumeID, old, result.RestoredImage(), result.RestoreRoles, nil)
		return err
	})
	return out, err
}

// DiscardRecoveryTarget deletes the volume an ended restore onto another server created, on that
// server while it is still the environment's. The volume becomes one of the environment's own
// first, so a draft that mounts it keeps it, and a cutover can't claim it once it is deleted.
func (s *Service) DiscardRecoveryTarget(ctx context.Context, p store.Principal, ws, pid, env, id, recovery uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "backup.recovery.discard", recovery, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		_, row, err := loadBackupRecovery(ctx, tx, ws, env, id, recovery)
		if err != nil {
			return err
		}
		// A restore that hasn't ended holds the environment's one active operation, so the
		// deletion can't be issued until it does. A draft or plan that mounts the volume refuses it
		// in observeVisual: a prepared plan applies only from the draft's revision.
		if v.Server == nil || *v.Server != row.ServerID {
			return domain.RestoreServerMoved
		}
		// Any delete of the volume, a discard or another, claims it until the delete ends. Rows are
		// locked in the order an agent's report locks them: the server's, then the environment's,
		// which observeVisual decides the delete under, then the volume's in ClaimRestoreTarget.
		if _, err = s.visualServer(ctx, tx, ws, row.ServerID); err != nil {
			return err
		}
		deletions, err := store.VolumeDeletions(ctx, db.New(tx), ws, env, []uuid.UUID{row.TargetVolumeID})
		if err != nil {
			return err
		}
		if deletions[row.TargetVolumeID] == store.VolumeDeleting {
			return domain.DiscardActive
		}
		_, err = db.New(tx).ClaimRestoreTarget(ctx, db.ClaimRestoreTargetParams{WorkspaceID: ws, EnvironmentID: env, UnitID: v.Unit, ID: row.TargetVolumeID})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.VolumeDeleted
		}
		if err != nil {
			return err
		}
		if out, err = s.observeVisual(ctx, tx, ws, env, v, &row.TargetVolumeID); err != nil {
			return err
		}
		return db.New(tx).SetBackupRecoveryDiscard(ctx, db.SetBackupRecoveryDiscardParams{WorkspaceID: ws, EnvironmentID: env, ID: recovery, DiscardOperationID: &out.Id})
	})
	return operationFor(p, out), err
}

// loadBackupRecovery reads a policy and one of its restores onto another server.
func loadBackupRecovery(ctx context.Context, tx pgx.Tx, ws, env, id, recovery uuid.UUID) (backup.Policy, db.BackupRecoveryRow, error) {
	source, err := loadBackupPolicy(ctx, tx, ws, env, id)
	if err != nil {
		return source, db.BackupRecoveryRow{}, err
	}
	row, err := db.New(tx).BackupRecovery(ctx, db.BackupRecoveryParams{WorkspaceID: ws, EnvironmentID: env, PolicyID: id, ID: recovery})
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.NotFound
	}
	return source, row, err
}

const (
	recoveryUndeliveredNote = "Cancelled or expired before any agent received it."
	recoveryChangedNote     = "Stopped: the backup policy changed after this restore was started."
	recoveryRenewedNote     = "Stopped: the policy's server was renewed, and restores its own snapshots again."
	recoveryUnsupportedNote = "Stopped: this server's agent no longer runs restores. Upgrade it and start the restore again."
)

// hydrateRecovery decides a restore command's delivery and, with keys, fills its repository
// access. The access goes only to the server the restore was recorded for, as the operation it was
// issued as, while the policy is the generation it froze and its server is still revoked, and first
// only to an agent that runs the restore kind. A restore that can't go ahead never fails or skips
// the poll, which would hold or reorder the commands after it: one no agent has received ends
// here, and one an agent may hold is stopped without values.
func (s *Service) hydrateRecovery(ctx context.Context, a Identity, keys valueKeys, c *api.ExecutionCommand) (deliver, carried bool, err error) {
	var r backup.Recovery
	if c.Backup == nil || json.Unmarshal(*c.Backup, &r) != nil || r.Digest() != c.PlanHash {
		return false, false, domain.Invalid("backup")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(ctx)
	// Reporting's lock order: the identity, then the operation.
	valid, err := lockAgentCredential(ctx, tx, a)
	if err != nil {
		return false, false, err
	}
	if !valid {
		return false, false, domain.Unauthenticated
	}
	ws, q := a.Workspace, db.New(tx)
	op, err := q.LockRecoveryDelivery(ctx, db.LockRecoveryDeliveryParams{WorkspaceID: ws, ID: c.Id, ServerID: a.ID})
	if err != nil {
		return false, false, err
	}
	if Terminal(api.ExecutionState(op.State)) {
		return false, false, nil
	}
	cancelled, expired := c.CancelRequested || op.CancelRequested, time.Now().After(c.Deadline)
	if !op.Held && (cancelled || expired) {
		// No agent has it, so it ends here rather than reaching one that may not know the kind.
		return false, false, finishUndeliveredRecovery(ctx, tx, c.Id, ws, "cancelled", recoveryUndeliveredNote)
	}
	if cancelled {
		c.CancelRequested = true
		return true, false, tx.Commit(ctx)
	}
	note, release, err := recoveryRefusal(ctx, tx, ws, r, c)
	if err != nil {
		return false, false, err
	}
	if note == "" && !op.Held {
		// An agent that runs the kind settles it, even when its restore prerequisites fail at a
		// heartbeat; one that doesn't, such as a downgraded one, would hold it unsettled.
		runs, err := recoveryRestore(ctx, tx, ws, c.ServerId)
		if err != nil {
			return false, false, err
		}
		if runs == nil {
			note = recoveryUnsupportedNote
		}
	}
	switch {
	case note != "" && !op.Held:
		return false, false, finishUndeliveredRecovery(ctx, tx, c.Id, ws, "failed", note)
	case note != "":
		if err = q.StopPossiblyDeliveredOperation(ctx, db.StopPossiblyDeliveredOperationParams{WorkspaceID: ws, ID: c.Id, Note: note}); err != nil {
			return false, false, err
		}
		c.CancelRequested = true
		return true, false, tx.Commit(ctx)
	case expired:
		// Past its deadline the agent decides: one that started it runs to its own limit, one that
		// holds it unstarted ends it as expired, and one that lost it says so (stopLostWork).
		return true, false, tx.Commit(ctx)
	}
	if keys != nil {
		if err = openRecoveryAccess(ctx, q, keys, ws, release, &r, c); err != nil {
			return false, false, err
		}
	}
	return true, true, tx.Commit(ctx)
}

// finishUndeliveredRecovery ends a restore no agent has received, with a note saying why ahead of
// what it already said, such as that its agent must be upgraded to receive it.
func finishUndeliveredRecovery(ctx context.Context, tx pgx.Tx, id, ws uuid.UUID, state, note string) error {
	if err := db.New(tx).FinishUndeliveredRecovery(ctx, db.FinishUndeliveredRecoveryParams{WorkspaceID: ws, ID: id, State: state, Note: note}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// recoveryRelease is the stored policy a restore is released under, with its sealed access.
type recoveryRelease struct {
	policy    backup.Policy
	encrypted []byte
}

// recoveryRefusal says why a restore can no longer be released, or returns what it is released
// under.
func recoveryRefusal(ctx context.Context, tx pgx.Tx, ws uuid.UUID, r backup.Recovery, c *api.ExecutionCommand) (string, recoveryRelease, error) {
	var out recoveryRelease
	stored, err := db.New(tx).BackupRecoveryAccess(ctx, db.BackupRecoveryAccessParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId, ID: r.RunID, OperationID: c.Id, ServerID: c.ServerId, PolicyID: c.ResourceId})
	if errors.Is(err, pgx.ErrNoRows) {
		return recoveryChangedNote, out, nil
	}
	if err != nil {
		return "", out, err
	}
	p := &out.policy
	if json.Unmarshal(stored.Configuration, p) != nil || p.Generation != r.Installation.Policy.Generation || p.DestinationID != r.Installation.Destination.ID {
		return recoveryChangedNote, out, nil
	}
	// A renewed source server writes to the same repository again, through its own schedule.
	revoked, err := serverRevoked(ctx, tx, ws, p.ServerID)
	if err != nil || !revoked {
		return recoveryRenewedNote, out, err
	}
	out.encrypted = stored.EncryptedAccess
	return "", out, nil
}

// openRecoveryAccess fills a released restore's repository and destination access.
func openRecoveryAccess(ctx context.Context, q *db.Queries, keys valueKeys, ws uuid.UUID, release recoveryRelease, r *backup.Recovery, c *api.ExecutionCommand) error {
	policy := release.policy
	if err := openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupPolicy, ID: c.ResourceId}, release.encrypted, &r.Installation.Access); err != nil {
		return err
	}
	destination, err := q.BackupDestinationAccess(ctx, db.BackupDestinationAccessParams{WorkspaceID: ws, ID: policy.DestinationID})
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
// which must be the snapshot and target it was issued for, a manifest the policy admits and, if it
// names one, a release Astilba pinned.
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
	if r.Restore == nil || c.Backup == nil || json.Unmarshal(*c.Backup, &v) != nil || json.Unmarshal(*r.Restore, &result) != nil || result.Snapshot != v.Snapshot || result.TargetID != v.TargetID || v.Admits(result.Manifest) != nil || unknownRestoreImage(result.RestoreImage) || unknownRestoreRoles(result.RestoreRoles) {
		return domain.Invalid("restore")
	}
	raw, err := stablejson.Marshal(result)
	if err != nil {
		return err
	}
	return db.New(tx).RecordBackupRecoveryResult(ctx, db.RecordBackupRecoveryResultParams{WorkspaceID: ws, OperationID: c.Id, Result: raw})
}
