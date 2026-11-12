package portability

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
)

// CheckRecoveryKeys verifies historical credential keys before metadata changes.
// Plaintext is discarded immediately and never enters the host handoff.
func CheckRecoveryKeys(keys *secretbroker.Session, a Archive) error {
	for _, table := range tables {
		for _, row := range a.Tables[table.Name] {
			field, item, err := credential(table.Name, row)
			if err != nil {
				return err
			}
			if field == "" {
				continue
			}
			raw, err := rowString(row, field)
			if err != nil || !strings.HasPrefix(raw, `\x`) {
				return ErrArchive
			}
			cipher, err := hex.DecodeString(raw[2:])
			if err != nil {
				return ErrArchive
			}
			plain, err := keys.Open(item, cipher)
			clear(plain)
			if err != nil {
				return errors.New("a historical credential key is missing from the management recovery keyring")
			}
		}
	}
	return nil
}
