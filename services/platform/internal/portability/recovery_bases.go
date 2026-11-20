package portability

import (
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// RecoveryRowKey identifies a row across copies of a workspace by its table's recovery key.
func RecoveryRowKey(table string, row Row) (string, error) {
	values := []string{}
	for _, column := range strings.Split(recoveryKeys(table), ",") {
		value, err := rowString(row, column)
		if err != nil {
			return "", err
		}
		values = append(values, value)
	}
	raw, _ := stablejson.Marshal(values)
	return string(raw), nil
}
