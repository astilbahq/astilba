package portability

import (
	"context"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
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
	// Recovery is a recovery base's applied history, from RecoveryArchiveVersion on.
	Recovery *RecoveryHistory `json:"recovery,omitzero"`
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

// secretEnvironments maps each secret in an archive to its environment, which a secret version's
// value is sealed for.
type secretEnvironments map[uuid.UUID]uuid.UUID

// environmentsOf reads which environment each of an archive's secrets belongs to.
func environmentsOf(a Archive) (secretEnvironments, error) {
	out := secretEnvironments{}
	for _, row := range a.Tables["secrets"] {
		var id, env uuid.UUID
		if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["environment_id"], &env) != nil {
			return nil, ErrArchive
		}
		out[id] = env
	}
	return out, nil
}

// credential names a row's sealed column and the value it holds, or "" for a table with none or a
// secret version whose value was erased, which holds nothing to open or seal. With environments,
// a secret version's item names its secret's environment, whose key it is sealed under.
func credential(table string, row Row, environments secretEnvironments) (string, secretbroker.Item, error) {
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
	if table == "secret_versions" {
		erased := string(row["erased_at"]) != "null"
		if erased != (string(row[field]) == "null") {
			return "", secretbroker.Item{}, ErrArchive
		}
		if erased {
			return "", secretbroker.Item{}, nil
		}
	}
	value, e := rowString(row, "id")
	if e != nil {
		return "", secretbroker.Item{}, e
	}
	id, e := uuid.Parse(value)
	if e != nil {
		return "", secretbroker.Item{}, ErrArchive
	}
	item := secretbroker.Item{Kind: kind, ID: id}
	if environments != nil && table == "secret_versions" {
		var secret uuid.UUID
		if json.Unmarshal(row["secret_id"], &secret) != nil || environments[secret] == uuid.Nil {
			return "", secretbroker.Item{}, ErrArchive
		}
		item.Environment = environments[secret]
	}
	return field, item, nil
}

// transformCredential replaces a row's value with what transform makes of it: the value opened for
// an export, or sealed for an import, by keys for the workspace being exported or created.
func transformCredential(table string, row Row, environments secretEnvironments, transform func(secretbroker.Item, []byte) ([]byte, error)) error {
	field, item, e := credential(table, row, environments)
	if e != nil || field == "" {
		return e
	}
	value, e := sealedColumn(row, field)
	if e != nil {
		return e
	}
	value, e = transform(item, value)
	// A value that does not open, or would not open once sealed, is the archive's fault; anything
	// else, such as an ended session, keys not made yet, or a key the restored database never had,
	// is not.
	if errors.Is(e, secretbroker.ErrKeyNotInBackup) {
		return e
	}
	if errors.Is(e, cryptobox.ErrKey) || errors.Is(e, secretbroker.ErrTooLarge) {
		return ErrArchive
	}
	if e != nil {
		return e
	}
	setValue(row, field, `\x`+hex.EncodeToString(value))
	clear(value)
	return nil
}

// sealedColumn is the sealed value a row's column holds, as PostgreSQL's hex form of bytea.
func sealedColumn(row Row, field string) ([]byte, error) {
	raw, err := rowString(row, field)
	if err != nil || !strings.HasPrefix(raw, `\x`) {
		return nil, ErrArchive
	}
	value, err := hex.DecodeString(raw[2:])
	if err != nil {
		return nil, ErrArchive
	}
	return value, nil
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
				if err := transformCredential(table.Name, row, nil, keys.Open); err != nil {
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
