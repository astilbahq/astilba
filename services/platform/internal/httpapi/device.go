package httpapi

import (
	"context"
	"net/http"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
)

const deviceCookiePath = "/api/v1/auth/login"

// deviceCookieName is one name per browser, so this cookie cannot pile up with accounts or say
// which ones have signed in here. The login path is the only place it is sent, which __Host-
// does not allow. __Secure- lets a host sharing the domain plant a cookie of this name, but
// that gains nothing, since every one sent is checked and a token only counts for its account.
func (s *Server) deviceCookieName() string {
	if s.Sessions.Cookie.Secure {
		return "__Secure-astilba_device"
	}
	return "astilba_device"
}

func (s *Server) presentedDevices(r *http.Request) []string {
	var tokens []string
	for _, cookie := range r.CookiesNamed(s.deviceCookieName()) {
		tokens = append(tokens, cookie.Value)
	}
	return tokens
}

// issueDevice records that this browser completed authentication, for browserSession to send
// with the response. It replaces any token this browser held for another account.
func (s *Server) issueDevice(ctx context.Context, a db.Account) error {
	token, err := s.Auth.IssueDeviceToken(a)
	if err != nil {
		return err
	}
	ctx.Value(sessionLoadKey{}).(*sessionLoad).device = &http.Cookie{
		Name:     s.deviceCookieName(),
		Value:    token,
		Path:     deviceCookiePath,
		MaxAge:   int(auth.DeviceTokenLifetime.Seconds()),
		Secure:   s.Sessions.Cookie.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	return nil
}
