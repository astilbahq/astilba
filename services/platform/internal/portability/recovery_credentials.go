package portability

import (
	"encoding/json/v2"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// UnopenedValue is a secret version recovery cannot open because the management database was
// restored from a backup taken before its environment's key existed.
type UnopenedValue struct {
	Environment, Secret, Version uuid.UUID
	// Generation is the generation of the environment's key it is sealed under.
	Generation int64
}

// CheckRecoveryKeys verifies historical credential keys before metadata changes, and returns the
// secret versions sealed under environment keys the restored database never had: recovery either
// stops for them or, accepted, restores them erased. Plaintext is discarded immediately and never
// enters the host handoff. The secret versions in stored, which the workspace already holds, are
// not opened: the merge keeps a stored version as it is, so it needs no key.
func CheckRecoveryKeys(keys *secretbroker.Session, a Archive, stored map[uuid.UUID]bool) ([]UnopenedValue, error) {
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
			if field == "" || table.Name == "secret_versions" && stored[item.ID] {
				continue
			}
			cipher, err := sealedColumn(row, field)
			if err != nil {
				return nil, err
			}
			plain, err := keys.Open(item, cipher)
			clear(plain)
			if errors.Is(err, secretbroker.ErrKeyDestroyed) {
				// Its environment was purged since: the merge restores it erased.
				continue
			}
			if errors.Is(err, secretbroker.ErrKeyNotInBackup) && item.Environment != uuid.Nil {
				value, sealed, err := environmentSealed(row, item, cipher)
				if err != nil {
					return nil, err
				}
				if sealed {
					unopened = append(unopened, value)
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

// EnvironmentSealedValues is the archive's secret versions sealed under their environments' keys.
func EnvironmentSealedValues(a Archive) ([]UnopenedValue, error) {
	environments, err := environmentsOf(a)
	if err != nil {
		return nil, err
	}
	var values []UnopenedValue
	for _, row := range a.Tables["secret_versions"] {
		field, item, err := credential("secret_versions", row, environments)
		if err != nil || field == "" {
			if err != nil {
				return nil, err
			}
			continue
		}
		cipher, err := sealedColumn(row, field)
		if err != nil {
			return nil, err
		}
		value, sealed, err := environmentSealed(row, item, cipher)
		if err != nil {
			return nil, err
		}
		if sealed {
			values = append(values, value)
		}
	}
	return values, nil
}

// environmentSealed reports whether a secret version's value is sealed under an environment's
// key, which must be its own.
func environmentSealed(row Row, item secretbroker.Item, cipher []byte) (UnopenedValue, bool, error) {
	header, err := cryptobox.ReadHeader(cipher)
	if err != nil || header.Scope != cryptobox.ScopeEnvironment {
		return UnopenedValue{}, false, nil
	}
	var secret uuid.UUID
	if header.Holder != item.Environment || json.Unmarshal(row["secret_id"], &secret) != nil {
		return UnopenedValue{}, false, ErrArchive
	}
	return UnopenedValue{Environment: item.Environment, Secret: secret, Version: item.ID, Generation: header.Generation}, true, nil
}
