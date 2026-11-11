package publicsuffix

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/idna"
)

//go:embed test_psl.txt
var officialTests string

// The pin is only as good as its record: the files must be the ones the script wrote
// with source.go, not a later hand edit.
func TestEmbeddedFilesMatchTheirPin(t *testing.T) {
	for name, c := range map[string][2]string{"list": {listData, listSHA256}, "tests": {officialTests, testsSHA256}} {
		sum := sha256.Sum256([]byte(c[0]))
		if hex.EncodeToString(sum[:]) != c[1] {
			t.Errorf("%s does not match source.go; run scripts/public-suffix-list.mjs", name)
		}
	}
	if _, e := parse(listData); e != nil {
		t.Fatal(e)
	}
}

// The list's own conformance vectors, from tests/test_psl.txt at the same commit.
func TestOfficialVectors(t *testing.T) {
	check := regexp.MustCompile(`^checkPublicSuffix\((null|'[^']*'), (null|'[^']*')\);$`)
	ascii := func(t *testing.T, quoted string) string {
		s, e := idna.ToASCII(strings.ToLower(strings.Trim(quoted, "'")))
		if e != nil {
			t.Fatalf("%s: %v", quoted, e)
		}
		return s
	}
	n := 0
	for line := range strings.Lines(officialTests) {
		m := check.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || m[1] == "null" {
			continue
		}
		n++
		domain := ascii(t, m[1])
		got, e := RegistrableDomain(domain)
		if m[2] == "null" {
			if e == nil {
				t.Errorf("RegistrableDomain(%q) = %q, want none", domain, got)
			}
		} else if want := ascii(t, m[2]); e != nil || got != want {
			t.Errorf("RegistrableDomain(%q) = %q, %v, want %q", domain, got, e, want)
		}
	}
	if n < 50 {
		t.Fatalf("read only %d vectors from test_psl.txt", n)
	}
}

func TestListedReportsWhetherARuleMatched(t *testing.T) {
	for _, c := range []struct {
		domain, suffix string
		listed         bool
	}{
		{"a.example.com", "com", true},
		{"a.github.io", "github.io", true},
		{"a.b.compute.amazonaws.com", "b.compute.amazonaws.com", true},
		// No rule names "internal", so only the default rule makes it a suffix.
		{"a.example.internal", "internal", false},
	} {
		if s, l := PublicSuffix(c.domain); s != c.suffix || l != c.listed {
			t.Errorf("PublicSuffix(%q) = %q, %v, want %q, %v", c.domain, s, l, c.suffix, c.listed)
		}
	}
}
