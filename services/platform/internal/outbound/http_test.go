package outbound

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestPublicAddressPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "0.1.2.3", "198.18.0.1", "192.0.2.1", "240.1.2.3", "::1", "fd00::1", "fe80::1", "fec0::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:a00:1::", "2001:db8::1"} {
		if Public(netip.MustParseAddr(raw)) {
			t.Errorf("special-use address accepted: %s", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !Public(netip.MustParseAddr(raw)) {
			t.Errorf("public address rejected: %s", raw)
		}
	}
}
func TestHTTPPolicyBlocksPrivateTargetsAndRedirects(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(204) }))
	defer target.Close()
	blocked := Client(false, time.Second)
	defer blocked.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", target.URL, nil)
	if response, err := blocked.Do(req); err == nil {
		response.Body.Close()
		t.Fatal("private request escaped public policy")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	allowed := Client(true, time.Second)
	defer allowed.CloseIdleConnections()
	if response, err := allowed.Get(redirect.URL); err == nil {
		response.Body.Close()
		t.Fatal("redirect followed")
	}
	if hits != 0 {
		t.Fatal("blocked endpoint received a request")
	}
}
