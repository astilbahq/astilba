package portability

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// baseSelection chooses the rows a recovery base carries, so its size follows the workspace's
// configuration and the recovery window rather than its age. Recovery merges only the newest
// base, and a restored backup taken inside its window keeps every row the base leaves out. So a
// base carries the rows recovery needs exact and that can differ between such a backup and this
// database:
//   - every standard environment, retired or not, and each preview environment that is still
//     live or changed since the window opened, with each carried environment's state rows;
//   - unfinished work and its children, the parents of reservations, standalone network removals
//     unfinished or ended inside the window, and the newest 100 operations;
//   - submissions and preview requests unfinished or changed inside the window, and pending automatic
//     deployments;
//   - the newest 100 audit events, each carried environment's newest 50 revisions, and each
//     backup policy's newest 100 restores onto another server, with the restore of each carried
//     operation;
//   - the build artifacts those configurations use (archivePinnedArtifacts);
//   - everything those rows name, as an export closes over it, which includes each environment's
//     runtime head and retirement;
//   - of each carried secret, its newest 10 versions, every version a carried row names
//     (keepNamedSecretVersions) and every erased version (baseSecretVersions).
//
// Operations that changed a runtime travel as the recovery section's applied history instead
// (RecoveryHistory), and roots and runtime changes that ended inside the window as its settled
// list. What else changed in the gap keeps the backup's copy: a build that ended there is fenced
// as cancelled, an ended root keeps its last phase, and recovery projects each environment's
// endpoints again from its runtime (projectCrossHostGrants). since is the window's start less the widest margin a recorded time is given (7 days),
// so the window's edge errs towards carrying a row.
func baseSelection(ctx context.Context, tx pgx.Tx, ws uuid.UUID, since time.Time) (map[string]string, error) {
	return selectArchive(ctx, tx, ws, []archiveStep{{baseEnvironments, []any{since}}, {baseSeeds, []any{since}}}, archiveStep{baseSecretVersions, []any{SecretVersionsKept}})
}

// A base carries every erased version of a carried secret, however old and whatever names it:
// recovery takes the base's erasure over a value the restored backup still holds (recoveryErasure),
// and it accepts a backup from before the window whose runtimes haven't changed since. Without it,
// recovering from such a backup would bring a retired value back. An erased row holds no value.
// inline-sql: chooses the secret versions in the base's transaction-local working set
const baseSecretVersions = `INSERT INTO archive_keep(kind,id) SELECT 'version',x.id FROM (
	SELECT v.id,v.erased_at,row_number() OVER (PARTITION BY v.secret_id ORDER BY v.created_at DESC,v.id DESC) n FROM secret_versions v
	JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id
	WHERE v.workspace_id=$1) x WHERE x.n<=$2 OR x.erased_at IS NOT NULL ON CONFLICT DO NOTHING`

// A preview environment is live while an open request names it or its runtime is applied. One
// retired inside the window is carried too, so a backup from inside the window can't show it live
// without the base saying it isn't.
// inline-sql: seeds the base's transaction-local working set
const baseEnvironments = `INSERT INTO archive_keep(kind,id) SELECT 'env',e.id FROM environments e WHERE e.workspace_id=$1 AND (e.purpose<>'preview' OR e.created_at>=$2
	OR EXISTS (SELECT 1 FROM preview_requests r WHERE r.workspace_id=$1 AND r.environment_id=e.id AND (r.state<>'deleted' OR r.updated_at>=$2))
	OR EXISTS (SELECT 1 FROM execution_operations o WHERE o.workspace_id=$1 AND o.id=e.runtime_operation_id AND (o.runtime_outcome='applied' OR COALESCE(o.runtime_recorded_at,o.created_at)>=$2)))`

// A standalone network removal is read as unfinished, ended inside the window, or ended without an
// end time, each a range of an index: as one condition with OR, the planner reads every network
// operation the workspace has.
// inline-sql: seeds the base's transaction-local working set
const baseSeeds = `INSERT INTO archive_keep(kind,id) SELECT DISTINCT kind,id FROM (
	SELECT 'op' AS kind,o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.state IN ('queued','dispatched','running','needs_attention')
	UNION ALL SELECT 'op',c.id FROM execution_operations c JOIN execution_operations p ON p.workspace_id=c.workspace_id AND p.id=c.parent_id WHERE c.workspace_id=$1 AND p.state IN ('queued','dispatched','running','needs_attention')
	UNION ALL SELECT 'op',n.id FROM (SELECT id FROM execution_operations WHERE workspace_id=$1 ORDER BY created_at DESC FETCH FIRST 100 ROWS WITH TIES) n
	UNION ALL SELECT 'op',r.parent_id FROM cross_host_reservations r WHERE r.workspace_id=$1
	UNION ALL SELECT 'op',o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND o.kind='network' AND o.command->'network'->>'action'='restrict'
		AND o.state IN ('queued','dispatched','running','needs_attention')
	UNION ALL SELECT 'op',o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND o.kind='network' AND o.command->'network'->>'action'='restrict'
		AND o.ended_at>=$2
	UNION ALL SELECT 'op',o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND o.kind='network' AND o.command->'network'->>'action'='restrict'
		AND o.ended_at IS NULL AND o.created_at>=$2
	UNION ALL SELECT 'submission',s.id FROM source_submissions s JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id WHERE s.workspace_id=$1 AND (s.state='queued' OR COALESCE(s.finished_at,s.created_at)>=$2)
	UNION ALL SELECT 'revision',x.id FROM (SELECT s.id,row_number() OVER (PARTITION BY s.environment_id ORDER BY s.created_at DESC,s.id DESC) n FROM specification_revisions s JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id WHERE s.workspace_id=$1) x WHERE x.n<=50
	UNION ALL SELECT 'preview',r.id FROM preview_requests r WHERE r.workspace_id=$1 AND (r.state<>'deleted' OR r.updated_at>=$2)
	UNION ALL SELECT 'run',d.id FROM pipeline_auto_deployments d WHERE d.workspace_id=$1 AND d.state='pending'
	UNION ALL SELECT 'recovery',x.id FROM (SELECT r.id,row_number() OVER (PARTITION BY r.policy_id ORDER BY r.created_at DESC,r.id) n FROM backup_recoveries r JOIN archive_keep k ON k.kind='env' AND k.id=r.environment_id WHERE r.workspace_id=$1) x WHERE x.n<=100
	UNION ALL SELECT 'audit',a.id FROM (SELECT id FROM audit_events WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100) a
) seed ON CONFLICT DO NOTHING`
