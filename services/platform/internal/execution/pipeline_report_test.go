package execution

import "testing"

func TestContainsSecret(t *testing.T) {
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
	} {
		if got := containsSecret(c.text, c.value); got != c.want {
			t.Errorf("%s: containsSecret(%q, %q) = %v", c.name, c.text, c.value, got)
		}
	}
}
