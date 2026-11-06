package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// minTrimmedSecret is the shortest value searched for without its surrounding whitespace,
// or across line breaks. Below it the form is likely to be ordinary text such as `postgres`
// or `production`, and since every stored version counts, a match on a word a file needs
// could not be cleared.
const minTrimmedSecret = 12

var (
	// A line break as stored or as JSON and .env files write it, `\n` or `\\n` inside a string.
	secretLineBreak = regexp.MustCompile(`(?:\\+[rn]|[\r\n])+`)
	// Everything that wrapping, indenting or escaping a value can add between its bytes.
	secretLayout = regexp.MustCompile(`(?:\\+[rnt]|\s)+`)
	// The shape of a PEM or OpenSSH body line. A line like this means nothing outside the
	// value. Other lines of a multi-line value don't count alone: PEM armour, a config line
	// such as `NODE_ENV=production` and a service account's token URI are shared by ordinary
	// files, which could then never be submitted. Those bodies wrap at 64 and 70 bytes, so
	// 32 skips only a body's short last line.
	secretKeyLine = regexp.MustCompile(`^[A-Za-z0-9+/]{32,}={0,2}$`)
)

// SecretSearchForm is the text that exposes a stored value: `prod-key-123\n` is exposed by
// `TOKEN="prod-key-123"`, because the whitespace is usually left by a paste. It contains no
// more than the stored value, so whatever exposes the value also matches this form.
func SecretSearchForm(value string) string {
	if trimmed := strings.TrimSpace(value); len(trimmed) >= minTrimmedSecret {
		return trimmed
	}
	return value
}

// SecretLines splits a stored value on its line endings.
func SecretLines(value string) []string {
	return strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
}

// SecretView is one reading of a text: as written, or with its URL encoding undone.
type SecretView struct {
	Text    string
	offsets []int
}

// Offset returns where byte i of the view begins in the text it was read from. i may be
// len(Text), so a match's end maps to the end of its last encoded byte.
func (v SecretView) Offset(i int) int {
	if v.offsets == nil {
		return i
	}
	return v.offsets[i]
}

// SecretViews returns every reading of text to search for a value. Undoing the encoding
// rather than encoding the value finds each encoder's choice of what to escape and either
// case of hex, and can't match text that doesn't decode to the value. `+` is a space only
// in a query string, so it gets its own reading.
func SecretViews(text string) []SecretView {
	views := []SecretView{{Text: text}}
	if strings.Contains(text, "%") {
		views = append(views, urlDecodedView(text, false))
	}
	if strings.Contains(text, "+") {
		views = append(views, urlDecodedView(text, true))
	}
	return views
}

func urlDecodedView(text string, plusIsSpace bool) SecretView {
	var out strings.Builder
	out.Grow(len(text))
	offsets := make([]int, 0, len(text)+1)
	for i := 0; i < len(text); {
		offsets = append(offsets, i)
		if text[i] == '%' && i+2 < len(text) {
			if decoded, err := strconv.ParseUint(text[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(decoded))
				i += 3
				continue
			}
		}
		if plusIsSpace && text[i] == '+' {
			out.WriteByte(' ')
		} else {
			out.WriteByte(text[i])
		}
		i++
	}
	return SecretView{Text: out.String(), offsets: append(offsets, len(text))}
}

// SecretText is a text prepared for checking against stored values, which a submission
// repeats for every stored version.
type SecretText struct {
	views  []SecretView
	layout []string
}

func NewSecretText(text string) SecretText {
	t := SecretText{views: SecretViews(text)}
	for _, view := range t.views {
		t.layout = append(t.layout, secretLayout.ReplaceAllString(view.Text, ""))
	}
	return t
}

// Exposes reports whether the text holds a stored value, so that it must not leave for a
// Git provider or a public build argument. A match may be blocked for good, because every
// stored version counts, so anything beyond the value itself has to be unmistakable: the
// whole of a multi-line value however it is laid out, or a line that is key material.
func (t SecretText) Exposes(value string) bool {
	form := SecretSearchForm(value)
	if form == "" {
		return false
	}
	for _, view := range t.views {
		if strings.Contains(view.Text, form) {
			return true
		}
	}
	trimmed := strings.TrimSpace(value)
	lines := secretLineBreak.Split(trimmed, -1)
	if whole := secretLayout.ReplaceAllString(trimmed, ""); len(lines) > 1 && len(whole) >= minTrimmedSecret && t.containsIgnoringLayout(whole) {
		return true
	}
	for _, line := range lines {
		if line = strings.TrimSpace(line); secretKeyLine.MatchString(line) && t.containsIgnoringLayout(line) {
			return true
		}
	}
	return false
}

func (t SecretText) containsIgnoringLayout(value string) bool {
	for _, text := range t.layout {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}
