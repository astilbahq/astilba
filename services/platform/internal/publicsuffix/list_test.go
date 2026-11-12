package publicsuffix

import (
	_ "embed"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/idna"
)

//go:embed test_psl.txt
var officialTests string

// The list's own conformance vectors, from tests/test_psl.txt at the pinned commit.
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

// An IP address is no site's name, as browsers and golang.org/x/net treat it.
func TestAddressesHaveNoRegistrableDomain(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "203.0.113.10", "::1"} {
		if got, e := RegistrableDomain(a); e == nil {
			t.Errorf("RegistrableDomain(%q) = %q, want none", a, got)
		}
	}
}

// PLAT-125: isolation must hold for every name below the base, so a rule that names one
// child, or an exception that undoes a wildcard, must not count.
func TestIsolates(t *testing.T) {
	r, e := parse(`net
*.wild.net
!wild.net
exact.net
*.parent.net
s.unlisted.net
*.children.net
!one.children.net
`)
	if e != nil {
		t.Fatal(e)
	}
	for domain, want := range map[string]bool{
		"exact.net":        true,
		"child.parent.net": true,
		"children.net":     true,
		"unlisted.net":     false,
		"wild.net":         false,
		"a.exact.net":      false,
	} {
		if got := r.isolates(domain); got != want {
			t.Errorf("isolates(%q) = %v, want %v", domain, got, want)
		}
	}
}
