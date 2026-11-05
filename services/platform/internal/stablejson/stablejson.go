// Package stablejson is the JSON encoding that must not change between releases.
//
// Agents and controllers recompute each other's digests from decoded values, and
// plans, journals and recovery bundles keep digests for years. Use Marshal for any
// value that is hashed, signed, stored or sent to a peer on another version; plain
// encoding/json/v2 output differs for nil slices and maps, omitempty, HTML
// characters, invalid UTF-8 and time.Duration. See docs/json-encoding.md.
package stablejson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	jsonv1 "encoding/json"
	"encoding/json/v2"
)

// Marshal encodes v as encoding/json v1 does, byte for byte.
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v, jsonv1.DefaultOptionsV1())
}

// Normalize rewrites one JSON document with sorted object names and v1 escaping,
// keeping every number's text. Input that does not parse comes back unchanged.
// Recovery row hashes compare a row with the same row after a PostgreSQL round
// trip, so they ignore formatting but not number precision. Only the v1 decoder
// can read numbers as text into an empty interface.
func Normalize(raw []byte) []byte {
	var value any
	d := jsonv1.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return raw
	}
	normalized, _ := Marshal(value)
	return normalized
}

// Hash is the hex SHA-256 of Marshal's output. A value that cannot be encoded
// hashes as empty input, as every digest has since the first release.
func Hash(v any) string {
	b, _ := Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
