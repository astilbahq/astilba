package portability

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Row map[string]json.RawMessage
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
func setValue(row Row, key string, value any) { row[key], _ = json.Marshal(value) }
func credential(table string, row Row, workspace uuid.UUID) (string, string, error) {
	var field, purpose string
	switch table {
	case "secret_versions":
		field = "encrypted_value"
	case "git_connections":
		field = "encrypted_access"
		purpose = "git"
	case "registries":
		field = "encrypted_access"
		purpose = "registry"
	case "build_secret_versions":
		field = "encrypted_value"
		purpose = "build-secret"
	case "backup_destinations":
		field = "encrypted_access"
		purpose = "backup-destination"
	case "backup_policies":
		field = "encrypted_access"
		purpose = "backup-policy"
	default:
		return "", "", nil
	}
	value, e := rowString(row, "id")
	if e != nil {
		return "", "", e
	}
	id, e := uuid.Parse(value)
	if e != nil {
		return "", "", ErrArchive
	}
	if purpose == "" {
		return field, store.SecretIdentity(workspace, id), nil
	}
	return field, pipeline.EncryptionIdentity(workspace, id, purpose), nil
}
func (s *Service) transformCredential(table string, row Row, ws uuid.UUID, export bool) error {
	field, identity, e := credential(table, row, ws)
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
		value, e = s.Store.Keys.Open(value, identity)
	} else {
		value, e = s.Store.Keys.Seal(value, identity)
	}
	if e != nil {
		return ErrArchive
	}
	setValue(row, field, `\x`+hex.EncodeToString(value))
	clear(value)
	return nil
}
func (s *Service) Export(ctx context.Context, p store.Principal, ws uuid.UUID, passphrase string) ([]byte, error) {
	archive := Archive{Version: 5, Workspace: ws, CreatedAt: time.Now().UTC(), Tables: map[string][]Row{}}
	e := s.Store.InWorkspaceSnapshot(ctx, p, ws, func(tx pgx.Tx, q *db.Queries, w store.Workspace) error {
		var reserved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cross_host_reservations WHERE workspace_id=$1)`, ws).Scan(&reserved); err != nil {
			return err
		}
		if reserved {
			return domain.Conflict
		}
		archive.Name = w.Name
		archive.RequireMFA = w.RequireMFA
		var err error
		archive, err = captureArchive(ctx, tx, archive, true)
		if err != nil {
			return err
		}
		for _, table := range tables {
			for _, row := range archive.Tables[table.Name] {
				if err := s.transformCredential(table.Name, row, ws, true); err != nil {
					return err
				}
			}
		}
		return store.Audit(ctx, q, p, ws, "workspace.export_encrypted", ws)
	})
	if e != nil {
		return nil, e
	}
	plaintext, e := json.Marshal(archive)
	if e != nil {
		return nil, e
	}
	defer clear(plaintext)
	return Seal(ctx, passphrase, plaintext)
}
