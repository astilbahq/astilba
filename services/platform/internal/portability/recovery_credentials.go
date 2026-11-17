package portability

import (
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// UnopenedValue is a secret version recovery cannot open because the management database was
// restored from a backup taken before its environment's key existed.
type UnopenedValue struct {
	Environment, Secret, Version uuid.UUID
}

// CheckRecoveryKeys verifies historical credential keys before metadata changes, and returns the
// secret versions sealed under environment keys the restored database never had: recovery either
// stops for them or, accepted, restores them erased. Plaintext is discarded immediately and never
// enters the host handoff.
func CheckRecoveryKeys(keys *secretbroker.Session, a Archive) ([]UnopenedValue, error) {
	environments, err := environmentsOf(a)
	if err != nil {
		return nil, err
	}
	var unopened []UnopenedValue
	for _, table := range tables {
		for _, row := range a.Tables[table.Name] {
			field, item, err := credential(table.Name, row, environments)
			if err != nil {
				return nil, err
			}
			if field == "" {
				continue
			}
			raw, err := rowString(row, field)
			if err != nil || !strings.HasPrefix(raw, `\x`) {
				return nil, ErrArchive
			}
			cipher, err := hex.DecodeString(raw[2:])
			if err != nil {
				return nil, ErrArchive
			}
			plain, err := keys.Open(item, cipher)
			clear(plain)
			if errors.Is(err, secretbroker.ErrKeyDestroyed) {
				// Its environment was purged since: the merge restores it erased.
				continue
			}
			if errors.Is(err, secretbroker.ErrKeyNotInBackup) && item.Environment != uuid.Nil {
				if header, e := cryptobox.ReadHeader(cipher); e == nil && header.Scope == cryptobox.ScopeEnvironment {
					var secret uuid.UUID
					if header.Holder != item.Environment || json.Unmarshal(row["secret_id"], &secret) != nil {
						return nil, ErrArchive
					}
					unopened = append(unopened, UnopenedValue{Environment: item.Environment, Secret: secret, Version: item.ID})
					continue
				}
			}
			if secretbroker.SessionFailure(err) || errors.Is(err, secretbroker.ErrKeyNotInBackup) {
				return nil, err
			}
			if err != nil {
				return nil, errors.New("a historical credential key is missing from the management recovery keyring")
			}
		}
	}
	return unopened, nil
}
