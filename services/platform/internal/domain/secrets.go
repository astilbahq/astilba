package domain

import "strings"

// minTrimmedSecret is the shortest value searched for without its surrounding whitespace.
// Below it the trimmed form is likely to be ordinary text such as `postgres` or `production`,
// and since every stored version counts, a match on a word a file needs could not be cleared.
const minTrimmedSecret = 12

// SecretSearchForm is the text that exposes a stored value: `prod-key-123\n` is exposed by
// `TOKEN="prod-key-123"`, because the whitespace is usually left by a paste. It contains no
// more than the stored value, so whatever exposes the value also matches this form.
func SecretSearchForm(value string) string {
	if trimmed := strings.TrimSpace(value); len(trimmed) >= minTrimmedSecret {
		return trimmed
	}
	return value
}
