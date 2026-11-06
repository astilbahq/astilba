package domain

import (
	"net/url"
	"strings"
	"testing"
)

const (
	testKeyLine1 = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj"
	testKeyLine2 = "MzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu"
	testKeyLast  = "NXp98C3SC6dV"
	testKey      = "-----BEGIN PRIVATE KEY-----\n" + testKeyLine1 + "\n" + testKeyLine2 + "\n" + testKeyLast + "\n-----END PRIVATE KEY-----\n"
)

func TestSecretTextExposes(t *testing.T) {
	jsonEscapedKey := strings.ReplaceAll(testKey, "\n", `\n`)
	wrappedLine := testKeyLine1[:40] + "\n    " + testKeyLine1[40:]
	for _, c := range []struct {
		name, text, value string
		want              bool
	}{
		{"newline against a quoted value", `TOKEN="prod-key-123"`, "prod-key-123\n", true},
		{"space against a file name", "config/prod-key-123.env", "prod-key-123 ", true},
		{"LF against CRLF", "TOKEN=prod-key-123\r\n", "prod-key-123\n", true},
		{"leading whitespace", "TOKEN=prod-key-123", "\t prod-key-123", true},
		{"exact value without whitespace", "TOKEN=prod-key-123", "prod-key-123", true},
		{"absent value", "TOKEN=other", "prod-key-123\n", false},
		{"interior whitespace still counts", "abcd efgh", "abcd  efgh", false},
		{"at the floor", "abcdefghijkl", "abcdefghijkl\n", true},
		{"below the floor keeps ordinary text", "image: postgres:16", "postgres ", false},
		{"below the floor still matches as stored", "postgres \n", "postgres ", true},
		{"short value without whitespace", "compose.prod.yaml", "prod", true},
		{"only whitespace matches as stored", "a b", " ", true},
		{"only whitespace absent", "ab", " ", false},
		{"empty value", "anything", "", false},

		{"key in an indented YAML block", "key: |\n    " + strings.ReplaceAll(strings.TrimSpace(testKey), "\n", "\n    ") + "\n", testKey, true},
		{"key with CRLF line endings", strings.ReplaceAll(testKey, "\n", "\r\n"), testKey, true},
		{"key stored with CRLF against LF", testKey, strings.ReplaceAll(testKey, "\n", "\r\n"), true},
		{"key as a JSON string", `{"private_key": "` + jsonEscapedKey + `"}`, testKey, true},
		{"key stored JSON-escaped against real lines", testKey, jsonEscapedKey, true},
		{"key rewrapped", "-----BEGIN PRIVATE KEY-----\n" + testKeyLine1[:30] + "\n" + testKeyLine1[30:] + testKeyLine2 + testKeyLast + "\n-----END PRIVATE KEY-----", testKey, true},
		{"key body without its armour", "KEY=" + testKeyLine1 + testKeyLine2 + testKeyLast + "\n", testKey, true},
		{"one line of a key's body", "# " + testKeyLine2 + "\n", testKey, true},
		{"commented-out key", "# " + testKeyLine1 + "\n# " + testKeyLine2 + "\n", testKey, true},
		{"single-line key wrapped in a file", "key: >-\n    " + wrappedLine + "\n", testKeyLine1, true},
		{"multi-line value in an indented block", "config: |\n    host: db.internal\n    password: hunter2\n", "host: db.internal\npassword: hunter2\n", true},
		{"multi-line value URL encoded", "url=line-one-value%0Aline-two-value", "line-one-value\nline-two-value", true},

		{"another key's armour", "-----BEGIN PRIVATE KEY-----\nb3RoZXI=\n-----END PRIVATE KEY-----\n", testKey, false},
		{"a config line shared with a stored file", "environment:\n  NODE_ENV=production\n", "NODE_ENV=production\nAPI_KEY=abcdef\n", false},
		{"a long ordinary line shared with a stored file", `"token_uri": "https://oauth2.example.com/token/endpoint",`, "{\n" + `"token_uri": "https://oauth2.example.com/token/endpoint",` + "\n" + `"key": "x"` + "\n}\n", false},
		{"a key's short last line", "# " + testKeyLast + "\n", testKey, false},
		{"multi-line value below the floor", "abcdef", "abc\ndef", false},
		{"lines of a value in another order", "second-line-of-it first-line-of-it", "first-line-of-it\nsecond-line-of-it", false},
		{"short single-line value split by a line break", "password\nreset", "passwordreset", false},

		{"URL encoded in a connection string", "postgres://app:p%40ss-word-123@db/app", "p@ss-word-123", true},
		{"URL encoded in a file name", "config/p%40ss-word-123.env", "p@ss-word-123", true},
		{"URL encoded with lower-case hex", "url=a%2fb%2fc-d%2fe-123", "a/b/c-d/e-123", true},
		{"URL encoded leaving some reserved characters", "url=p%40ss!word-123", "p@ss!word-123", true},
		{"every byte URL encoded", "%70%40%73%73", "p@ss", true},
		{"space as plus", "q=my+pass+phrase", "my pass phrase", true},
		{"space as %20", "q=my%20pass%20phrase", "my pass phrase", true},
		{"plus kept literal among encoding", "q=a+b%20c-d-e-f", "a+b c-d-e-f", true},
		{"encoded plus with a plus for a space", "q=a%2Bb+c-d-e-f", "a+b c-d-e-f", true},
		{"stored whitespace around an encoded value", "p%40ss-word-123", "p@ss-word-123\n", true},
		{"short encoded value", "postgres://app:p%40ss@db/app", "p@ss", true},
		{"other encoded text", "url=p%40ss-word-456", "p@ss-word-123", false},
		{"percent sign without hex", "100%zz p@ss-word-12", "p@ss-word-123", false},
		{"percent sign at the end", "a%", "a%", true},
		{"truncated escape at the end", "p@ss%4", "p@ss@", false},
	} {
		if got := NewSecretText(c.text).Exposes(c.value); got != c.want {
			t.Errorf("%s: %q exposes %q = %v", c.name, c.text, c.value, got)
		}
	}
}

// The check and redaction read encoded text through the same views, which must recognise
// what the standard encoders produce for any byte a value can hold.
func TestSecretViewsReadEveryEncoderOutput(t *testing.T) {
	value := "a b+c/d:e@f?g&h=i%j!k'l(m)n*o;p,q$r~s-t_u.v\"w\\xé"
	for name, encoded := range map[string]string{
		"query":    url.QueryEscape(value),
		"path":     url.PathEscape(value),
		"userinfo": strings.TrimPrefix(url.UserPassword("", value).String(), ":"),
	} {
		if !NewSecretText("x=" + encoded + ";").Exposes(value) {
			t.Errorf("%s form %q was not found", name, encoded)
		}
	}
}

func TestSecretViewOffsetsCoverEncodedBytes(t *testing.T) {
	text := "x=p%40ss+y"
	views := SecretViews(text)
	if len(views) != 3 {
		t.Fatalf("got %d views", len(views))
	}
	view := views[1]
	if view.Text != "x=p@ss+y" {
		t.Fatalf("decoded to %q", view.Text)
	}
	start := strings.Index(view.Text, "@")
	if got := text[view.Offset(start):view.Offset(start+1)]; got != "%40" {
		t.Fatalf("offsets cover %q", got)
	}
	if view.Offset(len(view.Text)) != len(text) {
		t.Fatal("end of the view does not map to the end of the text")
	}
}
