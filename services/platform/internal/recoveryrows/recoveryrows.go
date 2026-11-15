// Package recoveryrows hashes rows as cross-host recovery compares them. Recovery treats some
// tables as immutable and compares a stored row with a checkpoint's copy of it. A row holding a
// sealed value is compared on its content form, which is unchanged when the value's key moves.
// The record of rows a recovery restored is kept in that form too.
package recoveryrows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// sealedTable is an immutable table whose rows hold a sealed value. Columns are what recovery
// reads of it, which the portability table list carries too; a test there keeps them equal.
type sealedTable struct {
	name, columns, sealed string
}

var sealedTables = []sealedTable{
	{"build_secret_versions", "id,workspace_id,name,encrypted_value,created_at", "encrypted_value"},
	{"secret_versions", "workspace_id,secret_id,id,encrypted_value,author_id,created_at,erased_at", "encrypted_value"},
}

// Columns is what recovery reads of an immutable table whose rows hold a sealed value, and "" for
// any other table.
func Columns(table string) string {
	for _, t := range sealedTables {
		if t.name == table {
			return t.columns
		}
	}
	return ""
}

// Hash is a row's hash as stored: every column, a sealed value's bytes included. Checkpoints name
// the rows they restored by it, and it is the form older releases recorded.
func Hash(raw []byte) string {
	h := sha256.Sum256(stablejson.Normalize(raw))
	return hex.EncodeToString(h[:])
}

// ContentHash is a row's hash with each sealed value replaced by a digest of its data ciphertext.
// Moving a value's key, by a rewrap, a conversion or a recovery, leaves that data as it is, so the
// hash still matches. The data is the AES-GCM ciphertext under the value's own random data key,
// with a random nonce, so equal values have different digests. It is Hash for a table without
// sealed values, and for a value that is not an envelope, such as an erased one.
func ContentHash(table string, raw []byte) string {
	i := slices.IndexFunc(sealedTables, func(t sealedTable) bool { return t.name == table })
	if i < 0 {
		return Hash(raw)
	}
	var row map[string]jsontext.Value
	if json.Unmarshal(raw, &row) != nil {
		return Hash(raw)
	}
	column := sealedTables[i].sealed
	digest, ok := dataDigest(row[column])
	if !ok {
		return Hash(raw)
	}
	row[column] = jsontext.Value(`"sealed-data:sha256:` + digest + `"`)
	content, err := stablejson.Marshal(row)
	if err != nil {
		return Hash(raw)
	}
	return Hash(content)
}

// dataDigest is the digest of a bytea column's envelope data, as PostgreSQL renders bytea in JSON.
func dataDigest(value jsontext.Value) (string, bool) {
	var text string
	if json.Unmarshal(value, &text) != nil || !strings.HasPrefix(text, `\x`) {
		return "", false
	}
	sealed, err := hex.DecodeString(text[2:])
	if err != nil {
		return "", false
	}
	var envelope struct {
		Data []byte `json:"data"`
	}
	if json.Unmarshal(sealed, &envelope) != nil || len(envelope.Data) == 0 {
		return "", false
	}
	h := sha256.Sum256(envelope.Data)
	return hex.EncodeToString(h[:]), true
}

// Rekey records a workspace's restored rows in their content form, where a release before it
// recorded them as stored. It reads the rows as recovery does, and changes only records whose hash
// still matches a row. Run it, in tx, before any of the workspace's stored values move to another
// key: once a value has moved, the record of it in the old form matches nothing. It is idempotent,
// and reports how many records it changed.
func Rekey(ctx context.Context, tx db.DBTX, ws uuid.UUID) (int64, error) {
	q := db.New(tx)
	var changed int64
	for _, t := range sealedTables {
		recorded, err := q.RecoveryAttributedHashes(ctx, db.RecoveryAttributedHashesParams{WorkspaceID: ws, TableName: t.name})
		if err != nil || len(recorded) == 0 {
			if err != nil {
				return changed, err
			}
			continue
		}
		wanted := map[string]bool{}
		for _, h := range recorded {
			wanted[h] = true
		}
		// inline-sql: identifiers come from the compiled table list above
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT to_jsonb(q) FROM (SELECT %s FROM %s WHERE workspace_id=$1) q", t.columns, t.name), ws)
		if err != nil {
			return changed, err
		}
		moves := map[string]string{}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return changed, err
			}
			if stored := Hash(raw); wanted[stored] {
				if content := ContentHash(t.name, raw); content != stored {
					moves[stored] = content
				}
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return changed, err
		}
		for stored, content := range moves {
			n, err := q.RekeyRecoveryRowAttribution(ctx, db.RekeyRecoveryRowAttributionParams{Content: content, WorkspaceID: ws, TableName: t.name, Previous: stored})
			if err != nil {
				return changed, err
			}
			changed += n
		}
	}
	return changed, nil
}

// RekeyAll is Rekey for every workspace with a recorded restored row, in tx.
func RekeyAll(ctx context.Context, tx db.DBTX) (int64, error) {
	names := make([]string, 0, len(sealedTables))
	for _, t := range sealedTables {
		names = append(names, t.name)
	}
	workspaces, err := db.New(tx).RecoveryAttributedWorkspaces(ctx, names)
	if err != nil {
		return 0, err
	}
	var changed int64
	for _, ws := range workspaces {
		n, err := Rekey(ctx, tx, ws)
		changed += n
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}
