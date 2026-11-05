package portability

import (
	"context"
	"encoding/json"
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

func captureArchive(ctx context.Context, tx pgx.Tx, archive Archive, bounded bool) (Archive, error) {
	archive.Tables = map[string][]Row{}
	size, count := 0, 0
	for _, table := range tables {
		limit := ""
		if bounded {
			limit = " LIMIT 10001"
		}
		// inline-sql: identifiers come from the compiled portability table list
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT to_jsonb(t) FROM (SELECT %s FROM %s WHERE workspace_id=$1%s) t ORDER BY to_jsonb(t)::text", table.Columns, table.Name, limit), archive.Workspace)
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
