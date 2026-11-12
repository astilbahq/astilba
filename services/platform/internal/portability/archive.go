package portability

import (
	"context"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Row map[string]jsontext.Value
type Archive struct {
	Version    int              `json:"version"`
	Workspace  uuid.UUID        `json:"workspace"`
	Name       string           `json:"name"`
	RequireMFA bool             `json:"requireMfa"`
	CreatedAt  time.Time        `json:"createdAt"`
	Tables     map[string][]Row `json:"tables"`
}
type Service struct{ Store *store.Store }

func rowString(row Row, key string) (string, error) {
	var value string
	if json.Unmarshal(row[key], &value) != nil {
		return "", ErrArchive
	}
	return value, nil
}
func setValue(row Row, key string, value any) { row[key], _ = stablejson.Marshal(value) }

// credential names a row's sealed column and the value it holds, or "" for a table with none.
func credential(table string, row Row) (string, secretbroker.Item, error) {
	var field string
	var kind secretbroker.Kind
	switch table {
	case "secret_versions":
		field, kind = "encrypted_value", secretbroker.SecretVersion
	case "git_connections":
		field, kind = "encrypted_access", secretbroker.Git
	case "registries":
		field, kind = "encrypted_access", secretbroker.Registry
	case "build_secret_versions":
		field, kind = "encrypted_value", secretbroker.BuildSecret
	case "backup_destinations":
		field, kind = "encrypted_access", secretbroker.BackupDestination
	case "backup_policies":
		field, kind = "encrypted_access", secretbroker.BackupPolicy
	default:
		return "", secretbroker.Item{}, nil
	}
	value, e := rowString(row, "id")
	if e != nil {
		return "", secretbroker.Item{}, e
	}
	id, e := uuid.Parse(value)
	if e != nil {
		return "", secretbroker.Item{}, ErrArchive
	}
	return field, secretbroker.Item{Kind: kind, ID: id}, nil
}

// transformCredential opens a row's value for an export, or seals it for an import, through
// keys for the workspace being exported or created.
func transformCredential(keys *secretbroker.Session, table string, row Row, export bool) error {
	field, item, e := credential(table, row)
	if e != nil || field == "" {
		return e
	}
	raw, e := rowString(row, field)
	if e != nil || !strings.HasPrefix(raw, `\x`) {
		return ErrArchive
	}
	value, e := hex.DecodeString(raw[2:])
	if e != nil {
		return ErrArchive
	}
	if export {
		value, e = keys.Open(item, value)
	} else {
		value, e = keys.Seal(item, value)
	}
	if secretbroker.SessionFailure(e) {
		return e
	}
	if e != nil {
		return ErrArchive
	}
	setValue(row, field, `\x`+hex.EncodeToString(value))
	clear(value)
	return nil
}

// AdmitExport refuses what Export would refuse before reading the workspace: an account that
// may not export it, or a workspace with a reservation. A request is refused this way before it
// waits for the archive turn, and Export checks again once it has the turn.
func (s *Service) AdmitExport(ctx context.Context, p store.Principal, ws uuid.UUID) error {
	return s.Store.InWorkspaceSnapshot(ctx, p, ws, func(_ pgx.Tx, q *db.Queries, _ store.Workspace) error {
		reserved, err := q.WorkspaceHasReservations(ctx, ws)
		if err != nil {
			return err
		}
		if reserved {
			return domain.Conflict
		}
		return nil
	})
}

func (s *Service) Export(ctx context.Context, p store.Principal, ws uuid.UUID, passphrase string) (_ []byte, failure error) {
	archive := Archive{Version: ArchiveVersion, Workspace: ws, CreatedAt: time.Now().UTC(), Tables: map[string][]Row{}}
	// The same access InWorkspaceSnapshot needs.
	keys, e := s.Store.BeginKeys(ctx, p, store.WorkspaceAccess{Write: true, Owner: true, Credential: true}, true, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Export, Kinds: secretbroker.CredentialKinds})
	if e != nil {
		return nil, e
	}
	defer func() { keys.Close(ctx, secretbroker.Done(failure)) }()
	e = s.Store.InWorkspaceSnapshot(ctx, p, ws, func(tx pgx.Tx, q *db.Queries, w store.Workspace) error {
		reserved, err := q.WorkspaceHasReservations(ctx, ws)
		if err != nil {
			return err
		}
		if reserved {
			return domain.Conflict
		}
		archive.Name = w.Name
		archive.RequireMFA = w.RequireMFA
		archive, err = captureArchive(ctx, tx, archive, captureExport)
		if err != nil {
			return err
		}
		for _, table := range tables {
			for _, row := range archive.Tables[table.Name] {
				if err := omitExportLogs(table.Name, row); err != nil {
					return err
				}
				if err := transformCredential(keys, table.Name, row, true); err != nil {
					return err
				}
			}
		}
		return store.Audit(ctx, q, p, ws, "workspace.export_encrypted", ws)
	})
	if e != nil {
		return nil, e
	}
	plaintext, e := stablejson.Marshal(archive)
	if e != nil {
		return nil, e
	}
	defer clear(plaintext)
	return Seal(ctx, passphrase, plaintext)
}
