package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// recoveryEffect is the checkpoint a restore records just before it creates its target volume. A
// restore stopped before it changed nothing; one stopped after it may have left its volume and
// containers.
const recoveryEffect = "restore-effect"

// executeRecovery restores a snapshot from a lost server's policy repository into a new volume on
// this server. Only the snapshot decides what is restored: its manifest is read out of it and
// checked against the policy before anything is downloaded or created, and the restored data is
// checked against that manifest. The volume is not used by anything until a reviewed cutover.
func (e *Engine) executeRecovery(ctx context.Context, c api.ExecutionCommand, r Record) error {
	var v backup.Recovery
	if c.Backup == nil || json.Unmarshal(*c.Backup, &v) != nil || v.Digest() != c.PlanHash || v.Installation.Policy.ID != c.ResourceId || v.Installation.Policy.ServerID != c.ServerId || v.Installation.Policy.EnvironmentID != c.EnvironmentId || v.Validate() != nil {
		return e.finish(c.Id, "failed", "Restore command identity or policy is invalid.")
	}
	p := v.Installation.Policy
	if r.Checkpoint == recoveryEffect {
		return e.settleRecovery(c.Id, p, v.RunID, "The restore stopped after it began changing this server.")
	}
	if e.cancelled(c.Id) || time.Now().After(c.Deadline) {
		return e.finish(c.Id, "cancelled", "Cancelled before the restore changed anything.")
	}
	parent := ctx
	started, err := e.Journal.startWork(c.Id)
	if err != nil {
		return err
	}
	r.StartedAt = started
	ctx, stop := e.commandContext(ctx, c, r.workEnd(v.Lifetime()))
	defer stop()
	runCtx, cancel := context.WithTimeout(ctx, backup.RestoreTimeout)
	defer cancel()
	effects := false
	stored, err := e.recoverSnapshot(runCtx, v, func() error {
		if err := runCtx.Err(); err != nil {
			return err
		}
		if err := e.checkpoint(c.Id, recoveryEffect); err != nil {
			return err
		}
		effects = true
		return nil
	})
	if parent.Err() != nil {
		// The agent is stopping. It settles the restore by its checkpoint when it starts again.
		return parent.Err()
	}
	if err != nil {
		var refused restoreRefusal
		switch {
		case effects && runCtx.Err() != nil:
			return e.settleRecovery(c.Id, p, v.RunID, "The restore was cancelled or reached its time limit.")
		case effects:
			return e.settleRecovery(c.Id, p, v.RunID, "The restore failed while restoring or verifying its data.")
		case ctx.Err() != nil:
			return e.finish(c.Id, "cancelled", "The restore was cancelled or expired before it changed anything.")
		case runCtx.Err() != nil:
			return e.finish(c.Id, "failed", "The restore reached its time limit before it changed anything.")
		case errors.As(err, &refused):
			return e.finish(c.Id, "failed", "Restore refused before anything was created: "+refused.Error()+".")
		}
		return e.finish(c.Id, "failed", "Restore failed before anything was created. Check the destination's access, that the snapshot still exists, and that this server runs the current deployment.")
	}
	raw, err := stablejson.Marshal(backup.RecoveryResult{Snapshot: v.Snapshot, TargetID: v.TargetID, Manifest: stored})
	if err != nil {
		return err
	}
	return e.Journal.Mutate(c.Id, func(record *Record) {
		result := jsontext.Value(raw)
		record.Report.Restore = &result
		settleReport(record, "succeeded", "Snapshot restored into a new volume and verified against its manifest. Nothing uses the volume until a reviewed cutover.")
	})
}

// recoverSnapshot checks the restore against this server and the snapshot, then restores it.
func (e *Engine) recoverSnapshot(ctx context.Context, v backup.Recovery, begin func() error) (backup.Manifest, error) {
	in := v.Installation
	if err := e.checkRestoreOwnership(ctx, in.Policy); err != nil {
		return backup.Manifest{}, err
	}
	// The database password is the one this server's own database was created with, never one
	// released with the command: the restored cluster replaces that database's volume at cutover.
	in.Access.DatabasePassword = ""
	if in.Policy.Method == "postgres-logical" {
		password, err := e.databasePassword(ctx, in.Policy)
		if err != nil {
			return backup.Manifest{}, restoreRefusal("this server's database is not running as the deployment describes")
		}
		in.Access.DatabasePassword = password
	}
	if err := e.prepareRepository(ctx, &in, false); err != nil {
		return backup.Manifest{}, err
	}
	m, archive, err := e.snapshotManifest(ctx, in, v.Snapshot)
	if err != nil {
		return backup.Manifest{}, err
	}
	if err = v.Admits(m); err != nil {
		return backup.Manifest{}, restoreRefusal(err.Error())
	}
	return m, e.restoreArchive(ctx, in, v.RunID, v.TargetID, v.Snapshot, archive, m, begin)
}

// settleRecovery ends a restore that may have created its volume or containers. Once nothing it
// started can still be running it has failed, whatever its volume holds: nothing uses that volume,
// and only a succeeded restore's can be cut over to. Otherwise it is left to inspect, and holds
// this host until resolve-run or retry settles it. Neither uses the repository's access, so the
// record no longer keeps it.
func (e *Engine) settleRecovery(id uuid.UUID, p backup.Policy, run uuid.UUID, cause string) error {
	if left := e.removeRunContainers(p, run); left != "" {
		return e.Journal.Mutate(id, func(r *Record) {
			settleReport(r, "needs_attention", cause+" Its cleanup could not be confirmed: "+left+". Inspect containers labelled com.astilba.run="+run.String()+", then run resolve-run for this policy and run.")
			redactCommand(&r.Command)
		})
	}
	return e.finish(id, "failed", cause+" Nothing it started is still running. Its target volume may hold partial data; nothing uses it.")
}
