package integration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"testing"
	"time"
)

func TestAuthenticationLimitsBehindTrustedProxy(t *testing.T) {
	f := setup(t)
	f.apiServer.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	// Each edge represents a different client; both reach the API over real TCP
	// from the same proxy address and append that client's IP to incoming XFF.
	blocked := proxyClient(t, f, "198.51.100.10")
	other := proxyClient(t, f, "198.51.100.11")
	// Start at the limit so the test exercises HTTP admission without spending
	// fifty password hashes on unrelated credential checks.
	for range 50 {
		if err := f.auth.Limit(context.Background(), "password:ip:198.51.100.10", 50, 15*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	b := f.browser(t)
	login := func(target, spoof string, status int) {
		t.Helper()
		r, err := newRequest(b, "POST", "/api/v1/auth/login", []byte(`{"email":"nobody@example.test","password":"wrong"}`))
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(target)
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		r.Header.Set("X-Forwarded-For", spoof)
		r.Header.Set("Forwarded", "for="+spoof)
		r.Header.Set("X-Real-IP", spoof)
		r.Header.Set("CF-Connecting-IP", spoof)
		res, err := b.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("login via %s: got %d, want %d", target, res.StatusCode, status)
		}
	}
	login(blocked.URL, "203.0.113.1", 429)
	login(other.URL, "203.0.113.1", 400)
	for i := range 3 {
		login(blocked.URL, fmt.Sprintf("203.0.113.%d", i+2), 429)
	}
	// Without proxy trust, direct clients cannot choose another budget using any
	// of the common forwarding headers.
	f.apiServer.TrustedProxies = nil
	for range 50 {
		if err := f.auth.Limit(context.Background(), "password:ip:127.0.0.1", 50, 15*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	login(f.server.URL, "198.51.100.99", 429)
}

func TestLoginIPv6RotationSharesSourceBudget(t *testing.T) {
	f := setup(t)
	f.apiServer.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	first := proxyClient(t, f, "2001:db8:1::1")
	rotated := proxyClient(t, f, "2001:db8:1::2")
	other := proxyClient(t, f, "2001:db8:2::1")
	// Leave one admission so real HTTP attempts cross the shared /64 boundary
	// without spending fifty password comparisons on fixture setup.
	for range 49 {
		if err := f.auth.Limit(context.Background(), "password:ip:2001:db8:1::/64", 50, 15*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	b := f.browser(t)
	for i, tc := range []struct {
		proxy  *httptest.Server
		status int
	}{{first, 400}, {rotated, 429}, {other, 400}} {
		r, err := newRequest(b, "POST", "/api/v1/auth/login", []byte(fmt.Sprintf(`{"email":"unknown-%d@example.test","password":"wrong"}`, i)))
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(tc.proxy.URL)
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		res, err := b.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("login attempt %d returned %d, want %d", i+1, res.StatusCode, tc.status)
		}
	}
}

func proxyClient(t *testing.T, f *fixture, client string) *httptest.Server {
	t.Helper()
	upstream, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		chain := r.In.Header.Get("X-Forwarded-For")
		if chain != "" {
			chain += ", "
		}
		r.Out.Header.Set("X-Forwarded-For", chain+client)
	}})
	t.Cleanup(proxy.Close)
	return proxy
}
