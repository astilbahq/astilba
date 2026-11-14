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
//   - the newest 100 audit events, and each carried environment's newest 50 revisions;
//   - every version of each carried secret;
//   - the build artifacts those configurations use (archivePinnedArtifacts);
//   - everything those rows name, as an export closes over it, which includes each environment's
//     runtime head and retirement.
//
// Operations that changed a runtime travel as the recovery section's applied history instead
// (RecoveryHistory), and roots and runtime changes that ended inside the window as its settled
// list. What else changed in the gap keeps the backup's copy: a build that ended there is fenced
// as cancelled, an ended root keeps its last phase, and recovery projects each environment's
// endpoints again from its runtime (projectCrossHostGrants). since is the window's start less the widest margin a recorded time is given (7 days),
// so the window's edge errs towards carrying a row.
func baseSelection(ctx context.Context, tx pgx.Tx, ws uuid.UUID, since time.Time) (map[string]string, error) {
	return selectArchive(ctx, tx, ws, []archiveStep{{baseEnvironments, []any{since}}, {baseSeeds, []any{since}}})
}

// A preview environment is live while an open request names it or its runtime is applied. One
// retired inside the window is carried too, so a backup from inside the window can't show it live
// without the base saying it isn't.
// inline-sql: seeds the base's transaction-local working set
const baseEnvironments = `INSERT INTO archive_keep(kind,id) SELECT 'env',e.id FROM environments e WHERE e.workspace_id=$1 AND (e.purpose<>'preview' OR e.created_at>=$2
	OR EXISTS (SELECT 1 FROM preview_requests r WHERE r.workspace_id=$1 AND r.environment_id=e.id AND (r.state<>'deleted' OR r.updated_at>=$2))
	OR EXISTS (SELECT 1 FROM execution_operations o WHERE o.workspace_id=$1 AND o.id=e.runtime_operation_id AND (o.runtime_outcome='applied' OR COALESCE(o.runtime_recorded_at,o.created_at)>=$2)))`

// inline-sql: seeds the base's transaction-local working set
const baseSeeds = `INSERT INTO archive_keep(kind,id) SELECT DISTINCT kind,id FROM (
	SELECT 'op' AS kind,o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.state IN ('queued','dispatched','running','needs_attention')
	UNION ALL SELECT 'op',c.id FROM execution_operations c JOIN execution_operations p ON p.workspace_id=c.workspace_id AND p.id=c.parent_id WHERE c.workspace_id=$1 AND p.state IN ('queued','dispatched','running','needs_attention')
	UNION ALL SELECT 'op',o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.created_at>=COALESCE((SELECT n.created_at FROM execution_operations n WHERE n.workspace_id=$1 ORDER BY n.created_at DESC OFFSET 99 LIMIT 1),'-infinity')
	UNION ALL SELECT 'op',r.parent_id FROM cross_host_reservations r WHERE r.workspace_id=$1
	UNION ALL SELECT 'op',o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.parent_id IS NULL AND o.kind='network' AND o.command->'network'->>'action'='restrict'
		AND (o.state IN ('queued','dispatched','running','needs_attention') OR COALESCE(o.ended_at,o.created_at)>=$2)
	UNION ALL SELECT 'submission',s.id FROM source_submissions s JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id WHERE s.workspace_id=$1 AND (s.state='queued' OR COALESCE(s.finished_at,s.created_at)>=$2)
	UNION ALL SELECT 'revision',x.id FROM (SELECT s.id,row_number() OVER (PARTITION BY s.environment_id ORDER BY s.created_at DESC,s.id DESC) n FROM specification_revisions s JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id WHERE s.workspace_id=$1) x WHERE x.n<=50
	UNION ALL SELECT 'preview',r.id FROM preview_requests r WHERE r.workspace_id=$1 AND (r.state<>'deleted' OR r.updated_at>=$2)
	UNION ALL SELECT 'run',d.id FROM pipeline_auto_deployments d WHERE d.workspace_id=$1 AND d.state='pending'
	UNION ALL SELECT 'audit',a.id FROM (SELECT id FROM audit_events WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100) a
) seed ON CONFLICT DO NOTHING`
