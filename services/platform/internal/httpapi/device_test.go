package httpapi

import (
	"net/http"
	"testing"

	"github.com/alexedwards/scs/v2"
)

// The device cookie keeps the session cookie's transport and is never readable by scripts.
func TestDeviceCookieAttributes(t *testing.T) {
	for _, tc := range []struct {
		secure bool
		name   string
	}{{true, "__Secure-astilba_device"}, {false, "astilba_device"}} {
		sessions := scs.New()
		sessions.Cookie.Secure = tc.secure
		cookie := (&Server{Sessions: sessions}).deviceCookie("token")
		if cookie.Name != tc.name || cookie.Value != "token" || cookie.Path != deviceCookiePath || cookie.Secure != tc.secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
			t.Fatalf("secure=%v: %+v", tc.secure, cookie)
		}
	}
}
