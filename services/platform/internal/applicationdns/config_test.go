package applicationdns

import (
	"strings"
	"testing"
)

// PLAT-125: generated names on a base the Public Suffix List does not isolate share one
// browser site across tenants, so hosted mode refuses them. A base that is itself on the
// list used to fail startup in every mode. The bases are long-standing list entries.
func TestValidateIsolationByMode(t *testing.T) {
	const origin = "https://console.example.com"
	for _, c := range []struct {
		name, base string
		isolated   bool
	}{
		{"listed exactly", "vercel.app", true},
		{"matched by a wildcard rule", "eu-west-1.compute.amazonaws.com", true},
		{"its children matched by a wildcard rule", "compute.amazonaws.com", true},
		{"unlisted", "apps.example.net", false},
		{"registrable below a listed suffix", "apps.vercel.app", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{Base: c.base}
			if cfg.Isolated() != c.isolated {
				t.Fatalf("Isolated() = %v, want %v", !c.isolated, c.isolated)
			}
			if e := cfg.Validate(origin, false); e != nil {
				t.Errorf("self-hosted: %v", e)
			}
			e := cfg.Validate(origin, true)
			if c.isolated && e != nil {
				t.Errorf("hosted: %v", e)
			}
			if !c.isolated && (e == nil || !strings.Contains(e.Error(), "Public Suffix List")) {
				t.Errorf("hosted: got %v, want a Public Suffix List refusal", e)
			}
		})
	}
}

func TestValidateKeepsAccountAndApplicationSitesApart(t *testing.T) {
	for _, c := range []struct {
		name, base, origin string
		ok                 bool
	}{
		{"separate roots", "apps.example.net", "https://console.example.com", true},
		{"same unlisted root", "apps.example.com", "https://console.example.com", false},
		{"base is the origin's root", "example.com", "https://console.example.com", false},
		{"origin inside an unlisted base", "apps.example.net", "https://console.apps.example.net", false},
		{"origin inside a listed base", "vercel.app", "https://console.vercel.app", false},
		{"origin is a listed base", "vercel.app", "https://vercel.app", false},
		{"loopback development origin", "apps.example.net", "http://127.0.0.1:8080", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := Config{Base: c.base}.Validate(c.origin, false)
			if c.ok != (e == nil) {
				t.Errorf("Validate = %v, want ok %v", e, c.ok)
			}
		})
	}
}

func TestIsolatedNeedsABase(t *testing.T) {
	if (Config{}).Isolated() {
		t.Fatal("no base cannot be isolated")
	}
	if e := (Config{}).Validate("https://console.example.com", true); e != nil {
		t.Fatalf("hosted without generated names: %v", e)
	}
}
