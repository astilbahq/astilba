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
	upstream, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Each edge represents a different client; both reach the API over real TCP
	// from the same proxy address and append that client's IP to incoming XFF.
	edge := func(client string) *httptest.Server {
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
	blocked := edge("198.51.100.10")
	other := edge("198.51.100.11")
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
