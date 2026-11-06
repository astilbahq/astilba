package domain

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// minTrimmedSecret is the shortest value searched for in any form other than as stored.
// Below it the form is likely to be ordinary text such as `postgres` or `production`, and
// since every stored version counts, a match on a word a file needs could not be cleared.
const minTrimmedSecret = 12

var (
	// A line break however a file writes it, with the indentation around it: real, as `\n`
	// or `\\n` inside a string, or after a `\` that continues a line.
	secretLineBreak = regexp.MustCompile(`\s*(?:(?:\\*[\r\n]|\\+[rn])(?:\s|\\+t)*)+`)
	secretEscape    = regexp.MustCompile(`\\+`)
	secretSpace     = regexp.MustCompile(`\s+`)
	// Text with no meaning apart from its value, which a long enough run of base64 can be
	// assumed to have. Wrapping such a value changes nothing about it.
	secretOpaque = regexp.MustCompile(`^[A-Za-z0-9+/]{32,}={0,2}$`)
	// Only these bodies count in part. Other lines of a stored value are shared by ordinary
	// files: `NODE_ENV=production`, a service account's token URI, or the body of a public
	// certificate stored beside its key.
	secretPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY[A-Z0-9 ]*-----(.*?)-----END `)
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

// SecretForms returns part as written and as the standard URL encoders write it. SecretViews
// finds these too, unless a stray `%` before the value is read as an escape of its first bytes.
func SecretForms(part string) []string {
	return []string{part, url.QueryEscape(part), strings.TrimPrefix(url.UserPassword("", part).String(), ":")}
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

// secretLineForm removes what quoting and indenting add, keeping where each line ends so
// that `admin\npassword` isn't found in the words `admin password`.
func secretLineForm(text string) string {
	return secretEscape.ReplaceAllString(secretLineBreak.ReplaceAllString(text, "\n"), "")
}

// SecretText holds the texts a stored value must not appear in, prepared once because a
// check repeats for every stored version. A match in one text can't span two.
type SecretText struct {
	written, views, lined, bare []string
}

func NewSecretText(texts ...string) SecretText {
	var t SecretText
	for _, text := range texts {
		if text == "" {
			continue
		}
		t.written = append(t.written, text)
		for _, view := range SecretViews(text) {
			lined := secretLineForm(view.Text)
			t.views = append(t.views, view.Text)
			t.lined = append(t.lined, lined)
			t.bare = append(t.bare, secretSpace.ReplaceAllString(lined, ""))
		}
	}
	return t
}

// Exposes reports whether a text holds a stored value, so that it must not leave for a Git
// provider or a public build argument. A match may block a file for good, so anything other
// than the whole value has to be unmistakably private.
func (t SecretText) Exposes(value string) bool {
	form := SecretSearchForm(value)
	if form == "" {
		return false
	}
	// Whitespace kept below the floor is part of what is searched for, and may be all that
	// separates it from ordinary text, so only the stored bytes count.
	if form != strings.TrimSpace(form) {
		return containsSecretForm(t.written, form)
	}
	for _, encoded := range SecretForms(form) {
		if containsSecretForm(t.views, encoded) {
			return true
		}
	}
	lined := secretLineForm(form)
	if len(lined) >= minTrimmedSecret && containsSecretForm(t.lined, lined) {
		return true
	}
	if bare := secretSpace.ReplaceAllString(lined, ""); secretOpaque.MatchString(bare) && containsSecretForm(t.bare, bare) {
		return true
	}
	for _, block := range secretPrivateKey.FindAllStringSubmatch(form, -1) {
		if t.exposesPrivateKeyBody(block[1]) {
			return true
		}
	}
	return false
}

// exposesPrivateKeyBody finds a key without its armour, wrapped any way, or a full line of
// it. The first line is left out alone: an OpenSSH key's is the same in every key of a type.
func (t SecretText) exposesPrivateKeyBody(body string) bool {
	lines := strings.Fields(secretLineForm(body))
	if joined := strings.Join(lines, ""); secretOpaque.MatchString(joined) && containsSecretForm(t.bare, joined) {
		return true
	}
	for i, line := range lines {
		if i > 0 && secretOpaque.MatchString(line) && containsSecretForm(t.bare, line) {
			return true
		}
	}
	return false
}

func containsSecretForm(texts []string, form string) bool {
	for _, text := range texts {
		if strings.Contains(text, form) {
			return true
		}
	}
	return false
}
