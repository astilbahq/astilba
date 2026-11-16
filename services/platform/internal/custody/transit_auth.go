package custody

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// lookup asks the server whether it still accepts token, by having it look itself up, which every
// token may do unless its policies say otherwise. It returns the HTTP status and the error.
func (t *Transit) lookup(ctx context.Context, token string) (int, error) {
	_, status, err := t.sendWithin(ctx, "/auth/token/lookup-self", token, nil, nil, false)
	return status, err
}

// currentToken returns a token to use, renewing or replacing it first when it is due. While the
// token still works, one call refreshes it and the others go on using it, and a refresh the key
// service could not answer leaves it in use until it expires: a call refreshRetry later tries again.
func (t *Transit) currentToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	token, refresh, expires := t.token, t.refresh, t.expires
	t.mu.Unlock()
	if token != "" && t.now().Before(refresh) {
		return token, nil
	}
	// A call that waits for another's sign-in spends its wait from this deadline, so when that one
	// fails, the calls queued behind it fail soon after rather than each signing in in turn.
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	valid := token != "" && t.now().Before(expires)
	if !valid {
		t.signIn.Lock()
	} else if !t.signIn.TryLock() {
		return token, nil
	}
	defer t.signIn.Unlock()
	t.mu.Lock()
	token, refresh, expires, renewable := t.token, t.refresh, t.expires, t.renewable
	t.mu.Unlock()
	if token != "" && t.now().Before(refresh) {
		return token, nil
	}
	valid = token != "" && t.now().Before(expires)
	if valid && renewable && t.renew(ctx, token) {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.token, nil
	}
	fresh, err := t.login(ctx)
	// A refused sign-in is never papered over: its credentials need fixing.
	if err != nil && valid && (errors.Is(err, ErrUnavailable) || errors.Is(err, ErrThrottled)) {
		// Calls go on with the token, and one tries again a little later, not at once.
		t.mu.Lock()
		if retry := t.now().Add(refreshRetry); t.token == token && retry.Before(t.expires) {
			t.refresh = retry
		}
		t.mu.Unlock()
		return token, nil
	}
	return fresh, err
}

// refreshRetry is how long calls go on with a token whose renewal and replacement both failed
// before one tries again.
const refreshRetry = 15 * time.Second

// replaceToken signs in again after refused was refused, unless another call already has.
func (t *Transit) replaceToken(ctx context.Context, refused string) (string, error) {
	// As in currentToken, the wait for another call's sign-in counts against the deadline.
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	t.signIn.Lock()
	defer t.signIn.Unlock()
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	if token != "" && token != refused {
		return token, nil
	}
	return t.login(ctx)
}

// renew extends the token, and reports whether it got a useful extension. A token near its maximum
// lifetime comes back with less than it had, and is replaced by a sign-in instead.
func (t *Transit) renew(ctx context.Context, token string) bool {
	var auth *transitAuth
	if _, _, err := t.send(ctx, "/auth/token/renew-self", token, map[string]string{}, &auth, false); err != nil || auth == nil {
		return false
	}
	t.mu.Lock()
	previous := t.lease
	t.mu.Unlock()
	if time.Duration(auth.LeaseDuration)*time.Second < previous/3 {
		return false
	}
	t.keep(token, auth)
	return true
}

// login signs in with the configured method, reading its credentials afresh. The caller holds
// signIn.
func (t *Transit) login(ctx context.Context) (string, error) {
	var body privateBody
	var err error
	switch t.config.Auth.Method {
	case "approle":
		var role, secret []byte
		if role, err = readCredential(t.config.Auth.RoleIDFile); err != nil {
			return "", err
		}
		defer clear(role)
		if secret, err = readCredential(t.config.Auth.SecretIDFile); err != nil {
			return "", err
		}
		defer clear(secret)
		body, err = privateJSON(member{"role_id", role}, member{"secret_id", secret})
	case "jwt":
		var jwt []byte
		if jwt, err = readCredential(t.config.Auth.JWTFile); err != nil {
			return "", err
		}
		defer clear(jwt)
		body, err = privateJSON(member{"role", []byte(t.config.Auth.Role)}, member{"jwt", jwt})
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDenied, err)
	}
	defer clear(body)
	var auth *transitAuth
	_, status, err := t.send(ctx, "/auth/"+t.config.Auth.Mount+"/login", "", body, &auth, false)
	if err != nil {
		// A refused sign-in is never the server being away, so it is not retried as one.
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			return "", fmt.Errorf("%w: Transit refused the sign-in: %v", ErrDenied, err)
		}
		return "", err
	}
	if auth == nil || auth.ClientToken == "" {
		return "", fmt.Errorf("%w: Transit returned no token", ErrDenied)
	}
	t.keep(auth.ClientToken, auth)
	return auth.ClientToken, nil
}

// keep stores a token, due for renewal two thirds of the way through its lease. A token without a
// lease never expires; it is treated as a three-hour one, so it is looked at again after two.
func (t *Transit) keep(token string, auth *transitAuth) {
	lease := time.Duration(auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = 3 * time.Hour
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token, t.renewable, t.lease = token, auth.Renewable, lease
	t.refresh, t.expires = now.Add(lease*2/3), now.Add(lease)
}

// readCredential reads a sign-in credential from a private file. The caller clears it.
func readCredential(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, fmt.Errorf("%w: %s must be a private regular file (mode 0600)", ErrDenied, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read %s", ErrDenied, path)
	}
	value := bytes.TrimSpace(raw)
	if len(value) == 0 {
		clear(raw)
		return nil, fmt.Errorf("%w: %s is empty", ErrDenied, path)
	}
	return value, nil
}
