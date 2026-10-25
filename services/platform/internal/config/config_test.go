package config

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestTrustedProxyConfiguration(t *testing.T) {
	t.Setenv("ASTILBA_DATABASE_URL", "postgres://example.invalid/platform")
	t.Setenv("ASTILBA_KEYRING", "keyring.json")
	t.Setenv("ASTILBA_KEYRING_JSON", "")
	t.Setenv("ASTILBA_HOSTED", "false")
	for _, tc := range []struct {
		value string
		want  []netip.Prefix
		bad   bool
	}{
		{value: ""},
		{value: "192.0.2.20/32, 2001:db8:1::1/64", want: []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32"), netip.MustParsePrefix("2001:db8:1::/64")}},
		{value: "192.0.2.20", bad: true},
		{value: "proxy.example.test/32", bad: true},
		{value: "192.0.2.20/32,", bad: true},
		{value: "::ffff:192.0.2.20/128", bad: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ASTILBA_TRUSTED_PROXIES", tc.value)
			c, err := Load()
			if (err != nil) != tc.bad || (!tc.bad && !reflect.DeepEqual(c.TrustedProxies, tc.want)) {
				t.Fatalf("trusted proxies %v, error %v", c.TrustedProxies, err)
			}
		})
	}
}

func TestConsoleEmailRequiresLocalDevelopment(t *testing.T) {
	t.Setenv("ASTILBA_DATABASE_URL", "postgres://example.invalid/platform")
	t.Setenv("ASTILBA_KEYRING", "keyring.json")
	t.Setenv("ASTILBA_KEYRING_JSON", "")
	t.Setenv("ASTILBA_SMTP_MODE", "console")
	for _, tc := range []struct {
		name, development, hosted, listen, origin string
		allowed                                   bool
	}{
		{"local", "true", "false", "127.0.0.1:54181", "http://127.0.0.1:54182", true},
		{"local IPv6", "true", "false", "[::1]:54181", "http://[::1]:54182", true},
		{"production", "false", "false", "127.0.0.1:8080", "https://app.example.test", false},
		{"implicit development", "", "false", "127.0.0.1:8080", "http://127.0.0.1:54182", false},
		{"hosted", "true", "true", "127.0.0.1:54181", "http://127.0.0.1:54182", false},
		{"public listener", "true", "false", "0.0.0.0:54181", "http://127.0.0.1:54182", false},
		{"remote origin", "true", "false", "127.0.0.1:54181", "http://app.example.test", false},
		{"https origin", "true", "false", "127.0.0.1:54181", "https://127.0.0.1:54182", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ASTILBA_DEVELOPMENT", tc.development)
			t.Setenv("ASTILBA_HOSTED", tc.hosted)
			t.Setenv("ASTILBA_LISTEN", tc.listen)
			t.Setenv("ASTILBA_ORIGIN", tc.origin)
			c, err := Load()
			if (err == nil) != tc.allowed {
				t.Fatalf("console email allowed=%v, error=%v", tc.allowed, err)
			}
			if tc.allowed && (!c.SMTP.Development || c.SMTP.Mode != "console") {
				t.Fatal("local email configuration was not passed to the mailer")
			}
		})
	}
}
