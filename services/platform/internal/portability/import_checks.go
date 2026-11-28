package portability

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
)

// MaxCounter is the largest counter an agent report or an import may carry. Later statements
// add to counters across workspaces' shared sweeps, so one near the bigint limit would fail
// every time it was used. No real installation gets anywhere close.
const MaxCounter = 1 << 53

// Limits on one imported value. Background work decodes these columns in the server process
// or rebuilds them in the database, using up to hundreds of times their size, and an archive
// is not held to the limits agents and the API are. Columns decoded by sweeps across every
// workspace get a limit just above what their real writers can produce.
const maxImportedValue = 8 << 20

var importedValueLimits = map[string]int{
	"backup_policies.configuration":      2 << 20, // a plan of 1 MiB and its command
	"backup_policies.status":             512 << 10,
	"backup_policies.snapshot_receipts":  32 << 10, // 128 snapshots and times
	"cross_host_operations.receipts":     2 << 20,
	"cross_host_runtime.inventory":       2 << 20, // a plan of 1 MiB and an observation
	"network_members.installed_manifest": 256 << 10,
	"servers.capabilities":               64 << 10,
	"servers.preflight":                  64 << 10,
	"supported_upgrades.status":          1 << 20,
	"visual_runtime.observation":         512 << 10,
}

func importedCounter(column string) bool {
	if strings.HasSuffix(column, "generation") || strings.HasSuffix(column, "sequence") {
		return true
	}
	switch column {
	case "approved_controller", "cleanup_controller", "auth_version", "cleanup_auth_version":
		return true
	}
	return false
}

// A real archive's numbers are PostgreSQL's own output, which is short and never uses an
// exponent. It stores 1e100000 in a few bytes but renders it as 100 KB of digits, so reading
// a small row back could take more than a gigabyte in the database every workspace shares.
func importedNumbersPlain(raw jsontext.Value) bool {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.ReadToken()
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
		if token.Kind() == '0' && (len(token.String()) > 64 || strings.ContainsAny(token.String(), "eE")) {
			return false
		}
	}
}

// checkImportedRow refuses values that stored data has no other reason to reject and
// that installation-wide work trusts. A chosen passphrase seals an archive, so its
// content is whatever the importer wrote.
func checkImportedRow(table string, row Row) error {
	for column, raw := range row {
		limit, limited := importedValueLimits[table+"."+column]
		if !limited {
			limit = maxImportedValue
		}
		if len(raw) > limit || !importedNumbersPlain(raw) {
			return ErrArchive
		}
		if !importedCounter(column) {
			continue
		}
		var value *int64
		if json.Unmarshal(raw, &value) != nil || value != nil && (*value < 0 || *value > MaxCounter) {
			return ErrArchive
		}
	}
	return nil
}

// checkRecoveryResult refuses a restore result that its own row could not have recorded, which the
// restore's listing would fail to read.
func checkRecoveryResult(row Row) error {
	if string(row["result"]) == "null" {
		return nil
	}
	ids := map[string]uuid.UUID{}
	for _, column := range []string{"policy_id", "environment_id", "target_volume_id"} {
		value, err := rowString(row, column)
		if err != nil {
			return err
		}
		if ids[column], err = uuid.Parse(value); err != nil {
			return ErrArchive
		}
	}
	snapshot, err := rowString(row, "snapshot")
	if err != nil {
		return err
	}
	_, err = backup.ParseRecoveryResult(row["result"], ids["policy_id"], ids["environment_id"], snapshot, ids["target_volume_id"])
	return err
}

// checkKeyInstalls refuses a storage key's installs unless they are the list activation writes:
// each a policy and the generation whose install carries the key. The key sweep reads every
// workspace's installs in one statement, so one malformed list would stop it everywhere.
func checkKeyInstalls(raw jsontext.Value) error {
	var installs []struct {
		Policy     uuid.UUID `json:"policy"`
		Generation int64     `json:"generation"`
	}
	if json.Unmarshal(raw, &installs, json.RejectUnknownMembers(true)) != nil || installs == nil {
		return ErrArchive
	}
	for _, i := range installs {
		if i.Policy == uuid.Nil || i.Generation < 1 || i.Generation > MaxCounter {
			return ErrArchive
		}
	}
	return nil
}

// checkPolicyStatus refuses a backup policy status whose generation isn't a counter, since the key
// sweep and installs compare every workspace's reported generations in one statement.
func checkPolicyStatus(raw jsontext.Value) error {
	var status struct {
		Generation *jsontext.Value `json:"generation"`
	}
	// Only an object has a generation to read.
	if raw.Kind() != '{' {
		return nil
	} else if json.Unmarshal(raw, &status) != nil {
		return ErrArchive
	}
	var generation *int64
	if status.Generation != nil && (json.Unmarshal(*status.Generation, &generation) != nil || generation != nil && (*generation < 0 || *generation > MaxCounter)) {
		return ErrArchive
	}
	return nil
}
