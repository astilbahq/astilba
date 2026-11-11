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
	archive := Archive{Version: 6, Workspace: ws}
	header, err := db.New(tx).WorkspaceRecoveryHeader(ctx, ws)
	if err != nil {
		return nil, err
	}
	archive.Name, archive.RequireMFA, archive.CreatedAt = header.Name, header.RequireMfa, header.CapturedAt
	archive, err = captureArchive(ctx, tx, archive, true)
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
	archive := Archive{Version: 6, Workspace: ws}
	header, err := db.New(tx).WorkspaceRecoveryHeader(ctx, ws)
	if err != nil {
		return archive, "", err
	}
	archive.Name, archive.RequireMFA, archive.CreatedAt = header.Name, header.RequireMfa, header.CapturedAt
	archive, err = captureArchive(ctx, tx, archive, false)
	if err != nil {
		return archive, "", err
	}
	hash, err := recoveryStateHash(archive)
	return archive, hash, err
}

// retainedOperations is the operation history a bounded snapshot carries. Operation rows are never
// deleted, so a snapshot of all of them would eventually pass the archive's record limit and stop
// every cross-host deployment and export. A snapshot keeps:
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
// single-host backup commands. Its rows stay in this database, but a workspace imported from an
// export, or recovered from a backup older than them, doesn't get them back. RecoveryBases drops
// an older base's unfinished copy of one that a later base leaves out.
const retainedOperations = ` AND id IN (WITH RECURSIVE kept(id) AS (
	SELECT o.id FROM execution_operations o WHERE o.workspace_id=$1 AND (o.state NOT IN ('succeeded','failed','cancelled') OR o.runtime_outcome IS NOT NULL OR o.parent_id IS NULL AND o.kind='network' AND o.command->'network'->>'action'='restrict')
	UNION SELECT o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.created_at>=COALESCE((SELECT n.created_at FROM execution_operations n WHERE n.workspace_id=$1 ORDER BY n.created_at DESC OFFSET 99 LIMIT 1),'-infinity')
	UNION SELECT c.id FROM execution_operations c JOIN execution_operations p ON p.id=c.parent_id WHERE c.workspace_id=$1 AND p.state NOT IN ('succeeded','failed','cancelled')
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

// captureArchive reads every archived table. A bounded capture, which a recovery base and an export
// take, carries only retainedOperations and refuses past the archive's limits. An unbounded one is
// management's own view of its database, which recovery compares and merges against.
func captureArchive(ctx context.Context, tx pgx.Tx, archive Archive, bounded bool) (Archive, error) {
	archive.Tables = map[string][]Row{}
	size, count := 0, 0
	for _, table := range tables {
		filter := ""
		if bounded {
			if table.Name == "execution_operations" {
				filter = retainedOperations
			}
			filter += " LIMIT 10001"
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
