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
