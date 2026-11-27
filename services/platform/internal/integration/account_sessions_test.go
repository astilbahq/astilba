package integration

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

const firefoxOnMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.7; rv:133.0) Gecko/20100101 Firefox/133.0"

// agentTransport sends a User-Agent, as a browser does.
type agentTransport struct{ agent string }

func (a agentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", a.agent)
	return http.DefaultTransport.RoundTrip(r)
}

// signedInBrowser is a new browser signed in as the owner, sending agent as its User-Agent if
// one is given.
func signedInBrowser(t *testing.T, f *fixture, agent string) *browser {
	t.Helper()
	b := f.browser(t)
	if agent != "" {
		b.client.Transport = agentTransport{agent}
	}
	b.login("owner@example.test", f.password)
	return b
}

func accountSessions(t *testing.T, b *browser) api.AccountSessions {
	t.Helper()
	var out api.AccountSessions
	b.call("GET", "/api/v1/auth/sessions", nil, 200, &out)
	return out
}

// otherSession is the one listed session that is not b's.
func otherSession(t *testing.T, b *browser) api.AccountSession {
	t.Helper()
	listed := accountSessions(t, b).Sessions
	others := slices.DeleteFunc(slices.Clone(listed), func(s api.AccountSession) bool { return s.Current })
	if len(others) != 1 || len(listed) != 2 {
		t.Fatalf("expected the browser's session and one other, got %+v", listed)
	}
	return others[0]
}

func signedIn(t *testing.T, b *browser) bool {
	t.Helper()
	return b.session().User != nil
}

// The account page lists each signed-in session once, across its renewals, with when it signed
// in and a summary of its latest client that keeps only its network. A session not signed in, or
// one a credential change has moved on from, is not listed.
func TestAccountSessionsListTheSignedInSessions(t *testing.T) {
	f := setup(t)
	started := time.Now()
	owner := signedInBrowser(t, f, "")
	other := signedInBrowser(t, f, firefoxOnMac)
	f.browser(t).request("POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.test", "password": "wrong password"})

	listed := accountSessions(t, owner)
	if listed.Unlisted != 0 || len(listed.Sessions) != 2 || !listed.Sessions[0].Current && !listed.Sessions[1].Current {
		t.Fatalf("expected the two signed-in sessions, the browser's marked: %+v", listed)
	}
	seen := otherSession(t, owner)
	if seen.Browser != api.AccountSessionBrowserFirefox || seen.Os != api.AccountSessionOsMacos || seen.Network == nil || *seen.Network != "127.0.0.0/24" {
		t.Fatalf("the other session's client is %s on %s from %v", seen.Browser, seen.Os, seen.Network)
	}
	if seen.SignedInAt.Before(started.Add(-time.Second)) || seen.LastSeenAt.Before(seen.SignedInAt) {
		t.Fatalf("signed in at %v and last seen at %v, after %v", seen.SignedInAt, seen.LastSeenAt, started)
	}

	// A renewal continues the session: it keeps its place in the list and when it signed in.
	var renewed api.Session
	other.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": f.password}, 200, &renewed)
	other.csrf = renewed.CsrfToken
	if again := otherSession(t, owner); again.Id != seen.Id || !again.SignedInAt.Equal(seen.SignedInAt) || again.LastSeenAt.Before(seen.LastSeenAt) {
		t.Fatalf("renewal changed the session from %+v to %+v", seen, again)
	}

	// Changing the password renews only the session that changed it.
	var changed api.Session
	owner.call("POST", "/api/v1/auth/password", map[string]any{"password": "replacement password here", "current": map[string]string{"password": f.password}}, 200, &changed)
	owner.csrf = changed.CsrfToken
	if after := accountSessions(t, owner); len(after.Sessions) != 1 || !after.Sessions[0].Current {
		t.Fatalf("the moved-on session is still listed: %+v", after)
	}
}

// Signing out another session needs recent authentication, so a stolen session cannot sign its
// holder out. Doing it ends that session, records an account security event and tells the
// account holder.
func TestSigningOutAnotherSessionNeedsRecentAuthentication(t *testing.T) {
	f := setup(t)
	owner, _, _, _ := staleOwner(t, f, false)
	other := signedInBrowser(t, f, "")
	path := "/api/v1/auth/sessions/" + otherSession(t, owner).Id.String()
	for _, request := range []struct{ method, path string }{{"DELETE", path}, {"POST", "/api/v1/auth/sessions/sign-out-others"}} {
		if status, body := owner.request(request.method, request.path, nil); status != http.StatusForbidden || errorCode(t, body) != "recent_auth_required" {
			t.Fatalf("%s %s without recent authentication: %d %s", request.method, request.path, status, body)
		}
	}
	if !signedIn(t, other) || len(securityEvents(t, f)) != 0 {
		t.Fatal("a refused sign-out had an effect")
	}

	var session api.Session
	owner.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": f.password}, 200, &session)
	owner.csrf = session.CsrfToken
	var after api.AccountSessions
	owner.call("DELETE", path, nil, 200, &after)
	if len(after.Sessions) != 1 || !after.Sessions[0].Current {
		t.Fatalf("expected only the browser's own session afterwards: %+v", after)
	}
	if signedIn(t, other) {
		t.Fatal("the signed-out session is still signed in")
	}
	if events := securityEvents(t, f); !slices.Equal(events, []string{"sessions_signed_out"}) {
		t.Fatalf("security events %v", events)
	}
	awaitNotice(t, f, "Sessions of your Astilba account were signed out")
}

// Signing out every other session ends each, freeing an authenticator enrolment one of them
// began, in one account security event. The session that asks logs out instead, and one that is
// not the account's is not found.
func TestSigningOutOtherSessions(t *testing.T) {
	f := setup(t)
	owner := signedInBrowser(t, f, "")
	own := accountSessions(t, owner).Sessions[0].Id
	if status, body := owner.request("DELETE", "/api/v1/auth/sessions/"+own.String(), nil); status != http.StatusBadRequest || errorCode(t, body) != "invalid_input" {
		t.Fatalf("signing out the session that asks: %d %s", status, body)
	}
	if status, body := owner.request("DELETE", "/api/v1/auth/sessions/"+uuid.New().String(), nil); status != http.StatusNotFound {
		t.Fatalf("signing out a session that is not the account's: %d %s", status, body)
	}

	enrolling := signedInBrowser(t, f, "")
	enrollSecret(t, enrolling)
	third := signedInBrowser(t, f, "")
	if status, body := owner.request("POST", "/api/v1/auth/mfa/enroll", nil); status != http.StatusConflict || errorCode(t, body) != "mfa_setup_elsewhere" {
		t.Fatalf("beginning an enrolment while another session holds one: %d %s", status, body)
	}

	var after api.AccountSessions
	owner.call("POST", "/api/v1/auth/sessions/sign-out-others", nil, 200, &after)
	if len(after.Sessions) != 1 || after.Sessions[0].Id != own || after.Unlisted != 0 {
		t.Fatalf("expected only the browser's own session afterwards: %+v", after)
	}
	if signedIn(t, enrolling) || signedIn(t, third) {
		t.Fatal("another session is still signed in")
	}
	var pending bool
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT totp_pending IS NOT NULL OR totp_pending_lineage IS NOT NULL FROM accounts WHERE id=$1", f.owner).Scan(&pending); err != nil || pending {
		t.Fatalf("the signed-out session's enrolment is still pending: %v", err)
	}
	enrollSecret(t, owner)
	awaitNotice(t, f, "Sessions of your Astilba account were signed out")

	// With no other session left there is nothing to sign out, and nothing to tell anyone. A
	// session past its expiry, which the store has not yet cleaned up, is signed out already.
	expired := uuid.New()
	if _, err := f.st.Pool.Exec(context.Background(), `INSERT INTO sessions(token,data,expiry,lineage,account_id,auth_version,signed_in_at,last_seen_at,client_browser,client_os)
		SELECT 'expired', '\x00', now() - interval '1 minute', $2, a.id, a.auth_version, now() - interval '1 hour', now() - interval '31 minutes', 'other', 'other'
		FROM accounts a WHERE a.id = $1`, f.owner, expired); err != nil {
		t.Fatal(err)
	}
	if status, body := owner.request("DELETE", "/api/v1/auth/sessions/"+expired.String(), nil); status != http.StatusNotFound {
		t.Fatalf("signing out an expired session: %d %s", status, body)
	}
	owner.call("POST", "/api/v1/auth/sessions/sign-out-others", nil, 200, nil)
	if events := securityEvents(t, f); !slices.Equal(events, []string{"sessions_signed_out"}) {
		t.Fatalf("security events %v", events)
	}
}

// Another account's sessions are neither listed nor signed out: naming one is not found, and
// signing out every other session leaves them signed in.
func TestAnotherAccountsSessionsAreNotTheAccounts(t *testing.T) {
	f := setup(t)
	seedOtherAccount(t, f, "other@example.test")
	stranger := f.browser(t)
	stranger.login("other@example.test", f.password)
	theirs := accountSessions(t, stranger).Sessions[0].Id
	owner := signedInBrowser(t, f, "")
	if listed := accountSessions(t, owner); len(listed.Sessions) != 1 || !listed.Sessions[0].Current || listed.Unlisted != 0 {
		t.Fatalf("the owner's list holds another account's session: %+v", listed)
	}
	if status, body := owner.request("DELETE", "/api/v1/auth/sessions/"+theirs.String(), nil); status != http.StatusNotFound {
		t.Fatalf("signing out another account's session: %d %s", status, body)
	}
	owner.call("POST", "/api/v1/auth/sessions/sign-out-others", nil, 200, nil)
	if !signedIn(t, stranger) || len(securityEvents(t, f)) != 0 {
		t.Fatal("the owner's sign-out reached another account's session")
	}
}

// The list holds the session that asks and those seen most recently, and counts the rest, which
// signing out every other session ends too.
func TestSigningOutOtherSessionsEndsThoseNotListed(t *testing.T) {
	f := setup(t)
	owner := signedInBrowser(t, f, "")
	// The session that asks was last seen before every other, as one is after a while away.
	if _, err := f.st.Pool.Exec(context.Background(), `UPDATE sessions SET last_seen_at = now() - interval '3 hours' WHERE account_id = $1`, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(context.Background(), `INSERT INTO sessions(token,data,expiry,lineage,account_id,auth_version,signed_in_at,last_seen_at,client_browser,client_os)
		SELECT 'listed-' || i, '\x00', now() + interval '1 hour', gen_random_uuid(), a.id, a.auth_version, now() - interval '2 hours', now() - interval '1 hour', 'other', 'other'
		FROM accounts a, generate_series(1, 51) i WHERE a.id = $1`, f.owner); err != nil {
		t.Fatal(err)
	}
	listed := accountSessions(t, owner)
	if len(listed.Sessions) != 50 || listed.Unlisted != 2 || !listed.Sessions[0].Current {
		t.Fatalf("listed %d sessions, the first current: %v, and %d more", len(listed.Sessions), len(listed.Sessions) > 0 && listed.Sessions[0].Current, listed.Unlisted)
	}
	var after api.AccountSessions
	owner.call("POST", "/api/v1/auth/sessions/sign-out-others", nil, 200, &after)
	var left int
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE account_id=$1", f.owner).Scan(&left); err != nil || left != 1 || len(after.Sessions) != 1 || after.Unlisted != 0 {
		t.Fatalf("%d of the account's sessions left (%v), listed %+v", left, err, after)
	}
}
