package domain

import "github.com/astilbahq/astilba/services/platform/internal/stablejson"

// SecretLengths are how long a secret value is plain and as a JSON string, which is how a
// deployment payload carries it. Plans are checked against both without opening the value.
func SecretLengths(value []byte) (plain, json int) {
	escaped, _ := stablejson.Marshal(string(value))
	return len(value), len(escaped)
}
