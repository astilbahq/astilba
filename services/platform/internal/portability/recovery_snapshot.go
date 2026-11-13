package portability

import (
	"context"
	"encoding/json/v2"
	"fmt"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CaptureRecovery preserves existing credential ciphertext. Its result must be
// sealed with the management keyring before leaving the management process.
// This is an internal offline-recovery source, never a browser archive response.
func CaptureRecovery(ctx context.Context, tx pgx.Tx, ws uuid.UUID) ([]byte, error) {
	archive := Archive{Version: ArchiveVersion, Workspace: ws}
	header, err := db.New(tx).WorkspaceRecoveryHeader(ctx, ws)
	if err != nil {
		return nil, err
	}
	archive.Name, archive.RequireMFA, archive.CreatedAt = header.Name, header.RequireMfa, header.CapturedAt
	archive, err = captureArchive(ctx, tx, archive, captureBase)
	if err != nil {
		return nil, err
	}
	raw, err := stablejson.Marshal(archive)
	if err != nil || len(raw) > MaxArchiveBytes {
		return nil, ErrArchive
	}
	return raw, nil
}

// InspectRecovery reads trusted database state, including a union of individually
// bounded recovery bases. External decoding and new-base admission stay bounded.
func InspectRecovery(ctx context.Context, tx pgx.Tx, ws uuid.UUID) (Archive, string, error) {
	archive := Archive{Version: ArchiveVersion, Workspace: ws}
	header, err := db.New(tx).WorkspaceRecoveryHeader(ctx, ws)
	if err != nil {
		return archive, "", err
	}
	archive.Name, archive.RequireMFA, archive.CreatedAt = header.Name, header.RequireMfa, header.CapturedAt
	archive, err = captureArchive(ctx, tx, archive, captureAll)
	if err != nil {
		return archive, "", err
	}
	hash, err := recoveryStateHash(archive)
	return archive, hash, err
}

// retainedOperations is the operation history a recovery base carries. Operation rows are never
// deleted, so a base holding all of them would eventually pass the archive's record limit and stop
// every cross-host deployment. A base keeps:
//   - work that hasn't ended, and the children of a parent that hasn't;
//   - every operation that applied or removed a runtime, because recovery checks a newer base
//     against the exact operation an older base or backup applied;
//   - every standalone network removal, because recovery needs the last state of one that a
//     restored backup or an older base holds unfinished;
//   - every operation another archived row names, as the import's reference checks require;
//   - the newest 100, and any as old as the 100th, which covers the history the app lists;
//   - the parents of all of these.
//
// What's left is ended housekeeping, such as network configure and identify, agent update and
// single-host backup commands. Its rows stay in this database, but a workspace recovered from a
// backup older than them doesn't get them back. RecoveryBases drops an older base's unfinished
// copy of one that a later base leaves out. An export chooses its operations itself
// (exportSelection).
const retainedOperations = ` AND id IN (WITH RECURSIVE kept(id) AS (
	SELECT o.id FROM execution_operations o WHERE o.workspace_id=$1 AND (o.state IN ('queued','dispatched','running','needs_attention') OR o.runtime_outcome IS NOT NULL OR o.parent_id IS NULL AND o.kind='network' AND o.command->'network'->>'action'='restrict')
	UNION SELECT n.id FROM (SELECT id FROM execution_operations WHERE workspace_id=$1 ORDER BY created_at DESC FETCH FIRST 100 ROWS WITH TIES) n
	UNION SELECT c.id FROM execution_operations c JOIN execution_operations p ON p.id=c.parent_id WHERE c.workspace_id=$1 AND p.state IN ('queued','dispatched','running','needs_attention')
	UNION SELECT runtime_operation_id FROM environments WHERE workspace_id=$1
	UNION SELECT id FROM cross_host_operations WHERE workspace_id=$1
	UNION SELECT parent_id FROM cross_host_approvals WHERE workspace_id=$1
	UNION SELECT parent_id FROM cross_host_reservations WHERE workspace_id=$1
	UNION SELECT operation_id FROM visual_requests WHERE workspace_id=$1
	UNION SELECT id FROM pipeline_runs WHERE workspace_id=$1
	UNION SELECT operation_id FROM pipeline_auto_deployments WHERE workspace_id=$1
	UNION SELECT build_id FROM preview_requests WHERE workspace_id=$1
	UNION SELECT operation_id FROM preview_requests WHERE workspace_id=$1
	UNION SELECT o.parent_id FROM execution_operations o JOIN kept k ON k.id=o.id WHERE o.workspace_id=$1
) SELECT id FROM kept)`

// captureMode says which rows a capture reads.
type captureMode int

const (
	// captureAll is management's own view of its database, which recovery compares and merges
	// against. It is never sealed or exported, so it has no limits.
	captureAll captureMode = iota
	// captureBase is a recovery base. It keeps retainedOperations, and of the immutable history
	// recovery never reads, only what the app lists: the newest 100 audit events, and each
	// environment's newest 50 revisions plus every revision a carried row names.
	captureBase
	// captureExport is a workspace export, which carries what the app lists (exportSelection).
	captureExport
)

// baseFilters trims, in a recovery base, immutable tables that recovery never reads. Dropping a
// row a later base omits can't bring back an older copy of it, because these rows never change.
var baseFilters = map[string]string{
	"execution_operations":    retainedOperations,
	"audit_events":            ` AND id IN (SELECT id FROM audit_events WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100)`,
	"specification_revisions": ` AND id IN (` + referencedRevisions + ` UNION SELECT id FROM (SELECT id,row_number() OVER (PARTITION BY environment_id ORDER BY created_at DESC,id DESC) n FROM specification_revisions WHERE workspace_id=$1) r WHERE n<=50)`,
}

// referencedRevisions is every revision a foreign key names, from tables a base carries whole.
// inline-sql: a subquery of the compiled base filter for specification_revisions
const referencedRevisions = `SELECT draft_revision_id FROM environments WHERE workspace_id=$1
	UNION SELECT applied_revision_id FROM environments WHERE workspace_id=$1
	UNION SELECT revision_id FROM execution_plans WHERE workspace_id=$1
	UNION SELECT revision_id FROM visual_requests WHERE workspace_id=$1
	UNION SELECT imported_revision_id FROM git_sources WHERE workspace_id=$1
	UNION SELECT revision_id FROM pipeline_runs WHERE workspace_id=$1
	UNION SELECT revision_id FROM git_submissions WHERE workspace_id=$1
	UNION SELECT revision_id FROM source_submissions WHERE workspace_id=$1
	UNION SELECT revision_id FROM pipeline_auto_deployments WHERE workspace_id=$1
	UNION SELECT revision_id FROM preview_policies WHERE workspace_id=$1
	UNION SELECT revision_id FROM backup_cutovers WHERE workspace_id=$1`

// captureArchive reads every archived table. A recovery base or an export carries only what its
// mode keeps and refuses past the archive's limits.
func captureArchive(ctx context.Context, tx pgx.Tx, archive Archive, mode captureMode) (Archive, error) {
	archive.Tables = map[string][]Row{}
	filters := baseFilters
	if mode == captureExport {
		var err error
		if filters, err = exportSelection(ctx, tx, archive.Workspace); err != nil {
			return archive, err
		}
	}
	bounded := mode != captureAll
	size, count := 0, 0
	for _, table := range tables {
		filter := ""
		if bounded {
			filter = filters[table.Name] + " LIMIT 10001"
		}
		// inline-sql: identifiers come from the compiled portability table list
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT to_jsonb(t) FROM (SELECT %s FROM %s WHERE workspace_id=$1%s) t ORDER BY to_jsonb(t)::text", table.Columns, table.Name, filter), archive.Workspace)
		if err != nil {
			return archive, err
		}
		archive.Tables[table.Name] = []Row{}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return archive, err
			}
			count++
			size += len(raw)
			if bounded && (count > 10000 || size > MaxArchiveBytes) {
				rows.Close()
				return archive, ErrArchive
			}
			var row Row
			if err := json.Unmarshal(raw, &row); err != nil {
				rows.Close()
				return archive, err
			}
			archive.Tables[table.Name] = append(archive.Tables[table.Name], row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return archive, err
		}
	}
	return archive, nil
}
