package portability

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CaptureRecovery captures a recovery base with the full window. Its result preserves existing
// credential ciphertext and must be sealed with the management keyring before leaving the
// management process. It is an internal offline-recovery source, never a browser archive response.
func CaptureRecovery(ctx context.Context, tx pgx.Tx, ws uuid.UUID) ([]byte, error) {
	capture, err := CaptureRecoveryBase(ctx, tx, ws, RecoveryWindowTarget, AllChanges, nil)
	return capture.Raw, err
}

// RecoveryCapture is a captured base, the window its applied history covers, and how many changes
// inside the window that history holds, beside each environment's change from before it.
type RecoveryCapture struct {
	Raw         []byte
	CapturedAt  time.Time
	WindowStart time.Time
	Changes     int
}

// RecoveryCapturing carries what one capture learns into the next, when a base is captured again
// with a shorter window: the command identities it computed, by operation.
type RecoveryCapturing struct {
	identities map[uuid.UUID]string
}

// AllChanges asks CaptureRecoveryBase for every change in its window that fits.
const AllChanges = -1

// CaptureRecoveryBase captures a recovery base whose applied history reaches window back, or less
// when that doesn't fit the archive's limits, or holds more than changes changes (unless changes
// is AllChanges): the window then starts at the newest change that still fits, and the base carries
// only the retired previews and history that window needs. It refuses a workspace whose
// configuration and fixed history windows alone don't fit, or that can't fit a day of changes.
// The base is decoded as recovery will decode it before it is returned.
func CaptureRecoveryBase(ctx context.Context, tx pgx.Tx, ws uuid.UUID, window time.Duration, changes int, capturing *RecoveryCapturing) (RecoveryCapture, error) {
	if capturing == nil {
		capturing = &RecoveryCapturing{}
	}
	if capturing.identities == nil {
		capturing.identities = map[uuid.UUID]string{}
	}
	archive := Archive{Version: RecoveryArchiveVersion, Workspace: ws}
	header, err := db.New(tx).WorkspaceRecoveryHeader(ctx, ws)
	if err != nil {
		return RecoveryCapture{}, err
	}
	archive.Name, archive.RequireMFA, archive.CreatedAt = header.Name, header.RequireMfa, header.CapturedAt
	start := archive.CreatedAt.Add(-window)
	var selected time.Time
	size, count, settled := 0, 0, 0
	history := &RecoveryHistory{TargetDays: int(RecoveryWindowTarget / (24 * time.Hour))}
	// Each pass narrows the window or learns a larger settled list, so the window only moves later.
	for range 6 {
		if !start.Equal(selected) {
			filters, err := baseSelection(ctx, tx, ws, start.Add(-recoveryFallbackMargin))
			if err != nil {
				return RecoveryCapture{}, err
			}
			if archive, size, count, err = captureTables(ctx, tx, archive, filters, true); err != nil {
				return RecoveryCapture{}, err
			}
			if history.Superseded, err = recoverySuperseded(ctx, tx, ws); err != nil {
				return RecoveryCapture{}, err
			}
			// Before the ledger is read, the roots that ended inside the window bound the list.
			roots, err := recoverySettled(ctx, tx, ws, start, nil, true)
			if err != nil {
				return RecoveryCapture{}, err
			}
			settled, selected = max(settled, len(roots)), start
		}
		const listed = 160
		environments := len(archive.Tables["environments"])
		records := maxArchiveRecords - count - len(history.Superseded) - settled - 2*environments
		bytes := (MaxArchiveBytes - size - listed*(len(history.Superseded)+settled+environments)) / recoveryEntryBytes
		limit := min(records, bytes)
		if changes != AllChanges {
			limit = min(limit, changes+1)
		}
		next, err := recoveryWindowStart(ctx, tx, ws, start, limit)
		if err != nil {
			return RecoveryCapture{}, err
		}
		if !next.Before(archive.CreatedAt) {
			return RecoveryCapture{}, ErrArchive
		}
		if !next.Equal(start) {
			start = next
			continue
		}
		history.WindowStart = start
		if history.Environments, history.Applied, err = recoveryLedger(ctx, tx, ws, start, capturing.identities); err != nil {
			return RecoveryCapture{}, err
		}
		if history.Settled, err = recoverySettled(ctx, tx, ws, start, history.Applied, false); err != nil {
			return RecoveryCapture{}, err
		}
		if count+history.Records() > maxArchiveRecords {
			settled = len(history.Settled)
			continue
		}
		archive.Recovery = history
		raw, err := stablejson.Marshal(archive)
		if err != nil || len(raw) > MaxArchiveBytes {
			clear(raw)
			return RecoveryCapture{}, ErrArchive
		}
		if _, err = DecodeRecovery(raw); err != nil {
			clear(raw)
			return RecoveryCapture{}, err
		}
		floors := 0
		for _, e := range history.Environments {
			if !e.Whole {
				floors++
			}
		}
		return RecoveryCapture{Raw: raw, CapturedAt: archive.CreatedAt, WindowStart: start, Changes: len(history.Applied) - floors}, nil
	}
	return RecoveryCapture{}, ErrArchive
}

// InspectRecovery reads trusted database state, including a union of individually
// bounded recovery bases. External decoding and new-base admission stay bounded. Its recovery
// section holds only the applied history an earlier recovery kept without rows, and no window.
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
	// The state hash covers the tables; the applied history kept without rows is evidence only.
	hash, err := recoveryStateHash(archive)
	if err != nil {
		return archive, "", err
	}
	applied, err := RecoveredHistory(ctx, tx, ws)
	if err != nil {
		return archive, "", err
	}
	archive.Recovery = &RecoveryHistory{Applied: applied}
	return archive, hash, err
}

// captureMode says which rows a capture reads.
type captureMode int

const (
	// captureAll is management's own view of its database, which recovery compares and merges
	// against. It is never sealed or exported, so it has no limits.
	captureAll captureMode = iota
	// captureExport is a workspace export, which carries what the app lists (exportSelection).
	captureExport
)

// maxArchiveRecords is the most rows, and recovery section entries, an archive holds.
const maxArchiveRecords = 10000

// captureArchive reads every archived table. An export carries only what it selects and refuses
// past the archive's limits.
func captureArchive(ctx context.Context, tx pgx.Tx, archive Archive, mode captureMode) (Archive, error) {
	var filters map[string]string
	if mode == captureExport {
		var err error
		if filters, err = exportSelection(ctx, tx, archive.Workspace); err != nil {
			return archive, err
		}
	}
	archive, _, _, err := captureTables(ctx, tx, archive, filters, mode != captureAll)
	return archive, err
}

// captureTables reads each archived table through its filter, and reports the rows' size and
// number. A bounded capture refuses past the archive's limits.
func captureTables(ctx context.Context, tx pgx.Tx, archive Archive, filters map[string]string, bounded bool) (Archive, int, int, error) {
	archive.Tables = map[string][]Row{}
	size, count := 0, 0
	for _, table := range tables {
		filter := ""
		if bounded {
			filter = filters[table.Name] + fmt.Sprintf(" LIMIT %d", maxArchiveRecords+1)
		}
		// inline-sql: identifiers come from the compiled portability table list
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT to_jsonb(t) FROM (SELECT %s FROM %s WHERE workspace_id=$1%s) t ORDER BY to_jsonb(t)::text", table.Columns, table.Name, filter), archive.Workspace)
		if err != nil {
			return archive, 0, 0, err
		}
		archive.Tables[table.Name] = []Row{}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return archive, 0, 0, err
			}
			count++
			size += len(raw)
			if bounded && (count > maxArchiveRecords || size > MaxArchiveBytes) {
				rows.Close()
				return archive, 0, 0, ErrArchive
			}
			var row Row
			if err := json.Unmarshal(raw, &row); err != nil {
				rows.Close()
				return archive, 0, 0, err
			}
			archive.Tables[table.Name] = append(archive.Tables[table.Name], row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return archive, 0, 0, err
		}
	}
	return archive, size, count, nil
}
