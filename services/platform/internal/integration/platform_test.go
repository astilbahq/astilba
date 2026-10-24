package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/astilbahq/astilba/services/platform/internal/httpapi"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

func TestTenantRevisionsAndSecrets(t *testing.T) {
	f := setup(t)
	owner := f.browser(t)
	session := owner.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	var p api.Project
	owner.call("POST", fmt.Sprintf("/api/v1/workspaces/%s/projects", ws), map[string]string{"name": "Website"}, 201, &p)
	base := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s", ws, p.Id)
	var env api.Environment
	owner.call("POST", base+"/environments", map[string]string{"name": "Production"}, 201, &env)
	path := base + "/environments/" + env.Id.String()
	if env.Id == env.DeploymentUnit.Id {
		t.Fatal("environment and unit identity conflated")
	}
	var draft api.Revision
	owner.call("GET", path+"/draft", nil, 200, &draft)
	spec := map[string]any{"version": 1, "applications": []any{map[string]any{"id": uuid.NewString(), "name": "web", "source": map[string]string{"kind": "image", "image": "nginx:stable"}}}, "variables": []any{}}
	payload := map[string]any{"baseRevisionId": draft.Id, "specification": spec}
	// Independent requests using one base cannot silently overwrite one another.
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			data, _ := json.Marshal(payload)
			req, _ := newRequest(owner, "PUT", path+"/draft", data)
			res, e := owner.client.Do(req)
			if e != nil {
				statuses <- 0
				return
			}
			defer res.Body.Close()
			statuses <- res.StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	var current api.Revision
	owner.call("GET", path+"/draft", nil, 200, &current)
	appID := spec["applications"].([]any)[0].(map[string]any)["id"].(string)
	owner.call("PUT", path+"/layout", map[string]any{"positions": map[string]any{appID: map[string]int{"x": 10, "y": 20}}}, 200, nil)
	var after api.Revision
	owner.call("GET", path+"/draft", nil, 200, &after)
	if after.Id != current.Id {
		t.Fatal("layout changed spec revision")
	}
	if _, e := f.st.Pool.Exec(context.Background(), "UPDATE specification_revisions SET specification='{}' WHERE id=$1", current.Id); e == nil {
		t.Fatal("immutable revision accepted update")
	}
	var secret api.SecretVersion
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "sensitive-canary"}, 201, &secret)
	data := owner.call("GET", path+"/secrets", nil, 200, nil)
	if strings.Contains(string(data), "sensitive-canary") {
		t.Fatal("secret leaked in metadata")
	}
	var cipher []byte
	if e := f.st.Pool.QueryRow(context.Background(), "SELECT encrypted_value FROM secret_versions WHERE id=$1", secret.Id).Scan(&cipher); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(cipher), "sensitive-canary") {
		t.Fatal("secret stored plaintext")
	}
	plain, e := f.st.Keys.Open(cipher, store.SecretIdentity(ws, secret.Id))
	if e != nil || string(plain) != "sensitive-canary" {
		t.Fatal("cannot decrypt", e)
	}
	var other api.Workspace
	owner.call("POST", "/api/v1/workspaces", map[string]string{"name": "Other"}, 201, &other)
	owner.call("GET", strings.Replace(path, ws.String(), other.Id.String(), 1), nil, 404, nil)
	owner.call("GET", strings.Replace(path, p.Id.String(), uuid.NewString(), 1), nil, 404, nil)
	outsider := f.browser(t)
	outsider.call("GET", path, nil, 401, nil)
	// A different environment cannot pin a version belonging to this one.
	var env2 api.Environment
	owner.call("POST", base+"/environments", map[string]string{"name": "Preview"}, 201, &env2)
	spec["variables"] = []any{map[string]any{"name": "TOKEN", "secretVersionId": secret.Id}}
	owner.call("PUT", base+"/environments/"+env2.Id.String()+"/draft", map[string]any{"baseRevisionId": env2.DraftRevisionId, "specification": spec}, 400, nil)
}
func TestCredentialAndInvitationLifecycle(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	session := b.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	oldCSRF := b.csrf
	b.csrf = "wrong"
	b.call("POST", prefix+"/projects", map[string]string{"name": "blocked"}, 403, nil)
	b.csrf = oldCSRF
	b.call("POST", prefix+"/invitations", map[string]string{"email": "viewer@example.test", "role": "viewer"}, 201, nil)
	invite := tokenFromMail(t, f.mail)
	viewer := f.browser(t)
	var accepted api.Session
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": invite, "name": "Viewer", "password": f.password}, 200, &accepted)
	viewer.csrf = accepted.CsrfToken
	viewer.call("POST", prefix+"/projects", map[string]string{"name": "blocked"}, 403, nil)
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": invite, "name": "Viewer", "password": f.password}, 400, nil)
	b.call("DELETE", prefix+"/members/"+f.owner.String(), nil, 400, nil)
	// Enrol and prove the factor; its activation invalidates older authenticated sessions.
	other := f.browser(t)
	other.login("owner@example.test", f.password)
	var enrollment api.MFAEnrollment
	b.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, e := totp.GenerateCode(enrollment.Secret, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	var codes api.RecoveryCodes
	b.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &codes)
	b.csrf = codes.Session.CsrfToken
	if len(codes.Codes) != 10 {
		t.Fatal("missing recovery codes")
	}
	if other.session().User != nil {
		t.Fatal("old session survived credential version change")
	}
	login := f.browser(t)
	pending := login.login("owner@example.test", f.password)
	if !pending.MfaPending || pending.User != nil {
		t.Fatal("password alone completed MFA login")
	}
	login.call("POST", "/api/v1/auth/mfa", map[string]string{"code": code}, 400, nil) // activation consumed it
	var verified api.Session
	login.call("POST", "/api/v1/auth/mfa", map[string]string{"code": codes.Codes[0]}, 200, &verified)
	login.csrf = verified.CsrfToken
	replay := f.browser(t)
	replay.login("owner@example.test", f.password)
	replay.call("POST", "/api/v1/auth/mfa", map[string]string{"code": codes.Codes[0]}, 400, nil)
	b.call("POST", "/api/v1/auth/reset/request", map[string]string{"email": "owner@example.test"}, 202, nil)
	token := tokenFromMail(t, f.mail)
	newPassword := "new correct horse battery staple"
	b.call("POST", "/api/v1/auth/reset/complete", map[string]string{"token": token, "password": newPassword}, 204, nil)
	if login.session().User != nil {
		t.Fatal("reset left old session valid")
	}
	fresh := f.browser(t)
	fresh.call("POST", "/api/v1/auth/reset/complete", map[string]string{"token": token, "password": newPassword}, 400, nil)
	fresh.login("owner@example.test", newPassword)
	fresh.call("POST", "/api/v1/auth/mfa", map[string]string{"code": codes.Codes[1]}, 200, &verified)
}

func TestBootstrapAndOwnerRaces(t *testing.T) {
	f := setupBootstrap(t, false)
	ctx := context.Background()
	hash, e := f.auth.Passwords.Hash(ctx, f.password)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Go(func() { _, e := f.st.Bootstrap(ctx, "owner@example.test", "Owner", hash, "Initial"); results <- e })
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("bootstrap successes %d", success)
	}
	a := f.browser(t)
	session := a.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	f.owner = session.User.Id
	a.call("POST", prefix+"/invitations", map[string]string{"email": "other@example.test", "role": "owner"}, 201, nil)
	token := tokenFromMail(t, f.mail)
	b := f.browser(t)
	var second api.Session
	b.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": token, "name": "Second", "password": f.password}, 200, &second)
	b.csrf = second.CsrfToken
	statuses := make(chan int, 2)
	for _, pair := range []struct {
		client *browser
		target uuid.UUID
	}{{a, second.User.Id}, {b, f.owner}} {
		wg.Go(func() {
			req, _ := newRequest(pair.client, "DELETE", prefix+"/members/"+pair.target.String(), nil)
			res, e := pair.client.client.Do(req)
			if e != nil {
				statuses <- 0
				return
			}
			defer res.Body.Close()
			statuses <- res.StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	deleted := 0
	for status := range statuses {
		if status == 204 {
			deleted++
		} else if status != 404 && status != 400 {
			t.Fatalf("owner race status %d", status)
		}
	}
	if deleted != 1 {
		t.Fatal("owner race must remove exactly one")
	}
	var count int
	if e = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM memberships WHERE workspace_id=$1 AND role='owner'", ws).Scan(&count); e != nil || count != 1 {
		t.Fatal("last owner lost", count, e)
	}
}
func TestExistingAccountInviteAndMFAPolicy(t *testing.T) {
	f := setup(t)
	a := f.browser(t)
	session := a.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	var second api.Workspace
	a.call("POST", "/api/v1/workspaces", map[string]string{"name": "Second"}, 201, &second)
	a.call("POST", "/api/v1/workspaces/"+second.Id.String()+"/invitations", map[string]string{"email": "owner@example.test", "role": "viewer"}, 201, nil)
	token := tokenFromMail(t, f.mail)
	anonymous := f.browser(t)
	anonymous.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": token, "name": "Attacker", "password": "replacement password here"}, 403, nil)
	// Invitation redemption cannot replace an existing password or attach it to an anonymous session.
	probe := f.browser(t)
	probe.login("owner@example.test", f.password)
	a.call("PUT", "/api/v1/workspaces/"+ws.String(), map[string]any{"name": "Test workspace", "requireMfa": true}, 403, nil)
	f.st.Hosted = true
	a.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "blocked"}, 403, nil)
	// Backends set hardened cookie flags independently of their TLS-terminating proxy.
}

func TestOriginCookieAndRevocation(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	req, _ := newRequest(b, "POST", "/api/v1/auth/login", []byte(`{"email":"owner@example.test","password":"correct horse battery staple"}`))
	req.Header.Set("Origin", "https://untrusted.example")
	response, e := b.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin login accepted")
	}
	session := b.login("owner@example.test", f.password)
	if session.User == nil {
		t.Fatal("login failed")
	}
	var stored string
	if e = f.st.Pool.QueryRow(context.Background(), "SELECT token FROM sessions ORDER BY expiry DESC LIMIT 1").Scan(&stored); e != nil {
		t.Fatal(e)
	}
	for _, cookie := range b.client.Jar.Cookies(mustURL(f.server.URL)) {
		if stored == cookie.Value {
			t.Fatal("raw session token persisted")
		}
	}
	if _, e = f.st.Pool.Exec(context.Background(), "UPDATE accounts SET auth_version=auth_version+1 WHERE id=$1", f.owner); e != nil {
		t.Fatal(e)
	}
	if b.session().User != nil {
		t.Fatal("revoked session accepted")
	}
	production, stop := httpapi.New(f.st, f.auth, "https://example.test", false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer stop()
	handler, e := production.Handler(http.NotFoundHandler())
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	response, e = server.Client().Get(server.URL + "/api/v1/session")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-astilba_session" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" || cookies[0].Domain != "" {
		t.Fatal("session cookie lacks production protections")
	}
}
func mustURL(value string) *url.URL { u, _ := url.Parse(value); return u }

func seedInvitation(t *testing.T, f *fixture, email string) string {
	t.Helper()
	ctx := context.Background()
	ws, account := uuid.New(), uuid.New()
	if _, e := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: account, Email: account.String() + "@example.test", Name: "Other owner", PasswordHash: f.auth.Passwords.Dummy}); e != nil {
		t.Fatal(e)
	}
	if e := f.st.Queries.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: ws, Name: "Inviting workspace"}); e != nil {
		t.Fatal(e)
	}
	if e := f.st.Queries.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: ws, AccountID: account, Role: "owner"}); e != nil {
		t.Fatal(e)
	}
	token, e := cryptobox.Token()
	if e != nil {
		t.Fatal(e)
	}
	if e = f.st.Queries.InsertInvitation(ctx, db.InsertInvitationParams{WorkspaceID: ws, ID: uuid.New(), Email: email, Role: "viewer", TokenHash: auth.Hash(token), ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	return token
}
func TestInvitationPreservesExpiredRecentAuthentication(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	token := seedInvitation(t, f, "owner@example.test")
	future := time.Now().Add(6 * time.Minute)
	f.auth.Now = func() time.Time { return future }
	var session api.Session
	b.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": token}, 200, &session)
	b.csrf = session.CsrfToken
	if session.RecentAuth {
		t.Fatal("invitation upgraded authentication freshness")
	}
	b.call("POST", "/api/v1/auth/password", map[string]string{"password": "replacement password here"}, 403, nil)
}
func TestCredentialMutationsRejectRevokedRequestSnapshot(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	old, e := f.st.Queries.AccountByID(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	token := seedInvitation(t, f, old.Email)
	if e = f.st.Queries.BumpAuthVersion(ctx, f.owner); e != nil {
		t.Fatal(e)
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"password", func() error {
			_, e := f.auth.ChangePassword(ctx, f.owner, old.AuthVersion, "replacement password here")
			return e
		}},
		{"disable MFA", func() error { _, e := f.auth.DisableMFA(ctx, f.owner, old.AuthVersion); return e }},
		{"recovery", func() error { _, _, e := f.auth.RegenerateRecovery(ctx, f.owner, old.AuthVersion); return e }},
		{"enrollment", func() error { _, _, e := f.auth.BeginMFA(ctx, old); return e }},
		{"confirmation", func() error { _, _, e := f.auth.ConfirmMFA(ctx, f.owner, old.AuthVersion, "123456"); return e }},
		{"invitation", func() error { _, e := f.auth.AcceptInvitation(ctx, token, "", "", &old); return e }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if e := check.run(); !errors.Is(e, domain.Unauthenticated) {
				t.Fatalf("revoked snapshot accepted: %v", e)
			}
		})
	}
	current, e := f.st.Queries.AccountByID(ctx, f.owner)
	if e != nil || current.AuthVersion != old.AuthVersion+1 || current.TotpPending != nil {
		t.Fatal("revoked request changed credentials", e)
	}
}
func TestResetLocksAccountBeforeToken(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if e := f.auth.RequestReset(ctx, "owner@example.test", "test"); e != nil {
		t.Fatal(e)
	}
	token := tokenFromMail(t, f.mail)
	held, e := f.st.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer held.Rollback(ctx)
	if _, e = held.Exec(ctx, "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", f.owner); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- f.auth.CompleteReset(ctx, token, "replacement password here", "test") }()
	waiting := false
	for i := 0; i < 100; i++ {
		var count int
		e = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%-- name: LockAccount%' AND pid <> pg_backend_pid()").Scan(&count)
		if e != nil {
			t.Fatal(e)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("reset did not wait on account")
	}
	probe, e := f.st.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer probe.Rollback(ctx)
	if _, e = probe.Exec(ctx, "SELECT account_id FROM password_resets WHERE account_id=$1 FOR UPDATE NOWAIT", f.owner); e != nil {
		t.Fatal("reset held token before account", e)
	}
	_ = probe.Rollback(ctx)
	_ = held.Rollback(ctx)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reset blocked after locks released")
	}
}

func TestOperatorSessionAndInvitationExpiry(t *testing.T) {
	f := setup(t)
	owner := f.browser(t)
	before := owner.client.Jar.Cookies(mustURL(f.server.URL))[0].Value
	session := owner.login("owner@example.test", f.password)
	after := owner.client.Jar.Cookies(mustURL(f.server.URL))[0].Value
	if before == after {
		t.Fatal("login did not rotate session")
	}
	ws := session.Workspaces[0].Id
	owner.call("POST", "/api/v1/workspaces/"+ws.String()+"/invitations", map[string]string{"email": "operator@example.test", "role": "operator"}, 201, nil)
	token := tokenFromMail(t, f.mail)
	operator := f.browser(t)
	var accepted api.Session
	operator.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": token, "name": "Operator", "password": f.password}, 200, &accepted)
	operator.csrf = accepted.CsrfToken
	operator.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Operator project"}, 201, nil)
	operator.call("GET", "/api/v1/workspaces/"+ws.String()+"/members", nil, 403, nil)
	var foreign api.Workspace
	owner.call("POST", "/api/v1/workspaces", map[string]string{"name": "Private"}, 201, &foreign)
	operator.call("GET", "/api/v1/workspaces/"+foreign.Id.String(), nil, 404, nil)
	operator.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	if operator.session().User != nil {
		t.Fatal("logout preserved session")
	}
	expired := seedInvitation(t, f, "expired@example.test")
	if _, e := f.st.Pool.Exec(context.Background(), "UPDATE invitations SET expires_at=now()-interval '1 second' WHERE token_hash=$1", auth.Hash(expired)); e != nil {
		t.Fatal(e)
	}
	operator.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": expired, "name": "Expired", "password": f.password}, 400, nil)
	operator.call("POST", "/api/v1/bootstrap", nil, 404, nil)
}
