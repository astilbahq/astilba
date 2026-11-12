package integration

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A workspace's lock can be held for seconds by an export. Requests waiting for
// it must not take the connections every other tenant, River and /healthz share,
// whether they come from an outsider who knows the workspace's UUID or from the
// workspace's own browsers and agents.
func TestWorkspaceLockWaitersLeaveThePoolServing(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	owner := f.browser(t)
	ws := owner.login("owner@example.test", f.password).Workspaces[0].Id
	hash, err := f.auth.Passwords.Hash(f.password)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Email: "removed@example.test", Name: "Removed", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	outsider := f.browser(t)
	outsider.login("removed@example.test", f.password)
	server, enrollInput, _ := enrollment(t, f, owner, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", enrollInput, 200, nil)
	agent, err := f.execution.Authenticate(ctx, server.Server.Id, *enrollInput.Credential)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := cryptobox.Token()
	if err != nil {
		t.Fatal(err)
	}
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.Queries.InsertInvitation(ctx, db.InsertInvitationParams{WorkspaceID: ws, ID: uuid.New(), Email: "removed@example.test", Role: "viewer", TokenHash: auth.Hash(invitation), ExpiresAt: time.Now().Add(time.Hour), IssuerID: account.ID, IssuerAuthVersion: account.AuthVersion}); err != nil {
		t.Fatal(err)
	}

	principal := browserPrincipal(t, f.st, account, false)
	invited := browserPrincipal(t, f.st, removed, false)
	held, release, exported := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		exported <- f.st.InWorkspaceSnapshot(ctx, principal, ws, func(pgx.Tx, *db.Queries, store.Workspace) error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-exported:
		t.Fatal("snapshot was not admitted: ", err)
	}
	released := false
	unlock := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer unlock()

	send := func(b *browser, c context.Context, method, path string, body []byte) (int, error) {
		request, err := newRequest(b, method, path, body)
		if err != nil {
			return 0, err
		}
		response, err := b.client.Do(request.WithContext(c))
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		return response.StatusCode, nil
	}
	rename := func(b *browser, c context.Context) (int, error) {
		return send(b, c, "PUT", "/api/v1/workspaces/"+ws.String(), []byte(`{"name":"Renamed","requireMfa":false}`))
	}
	closeSessions := func(b *browser, c context.Context) (int, error) {
		return send(b, c, "POST", "/api/v1/workspaces/"+ws.String()+"/troubleshooting-sessions/close", nil)
	}
	waiters := int(f.st.Pool.Config().MaxConns)

	bounded, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	// An insert that only references the workspace, like a read route's audit event, has no reason to wait.
	if err := f.st.Queries.InsertAudit(bounded, db.InsertAuditParams{WorkspaceID: ws, ID: uuid.New(), ActorID: f.owner, Action: "environment.export", ResourceID: ws}); err != nil {
		t.Fatal("audit insert waited for the workspace lock: ", err)
	}
	outsiders := make(chan error, 2*waiters)
	for range waiters {
		for _, attempt := range []func(*browser, context.Context) (int, error){rename, closeSessions} {
			go func() {
				status, err := attempt(outsider, bounded)
				if err == nil && status != 404 {
					t.Errorf("outsider write status %d", status)
				}
				outsiders <- err
			}()
		}
	}
	for range 2 * waiters {
		if err := <-outsiders; err != nil {
			t.Fatal("outsider waited for another tenant's lock: ", err)
		}
	}

	network := jsontext.Value(`{}`)
	finished := make(chan struct{}, 4*waiters)
	for range waiters {
		for _, attempt := range []func(*browser, context.Context) (int, error){rename, closeSessions} {
			go func() {
				if status, err := attempt(owner, ctx); err != nil || status >= 300 {
					t.Errorf("queued member write: status %d: %v", status, err)
				}
				finished <- struct{}{}
			}()
		}
		go func() {
			_ = f.execution.Report(ctx, agent, api.AgentReport{Id: uuid.New(), Sequence: 1, State: "running", Network: &network})
			finished <- struct{}{}
		}()
		go func() {
			_, _ = f.auth.AcceptInvitation(ctx, invitation, "", "", auth.Source{Key: "test", Network: "test"}, &invited)
			finished <- struct{}{}
		}()
	}
	// Without the queue these waiters saturate the pool well inside this window.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		response, err := http.Get(f.server.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatalf("/healthz status %d with %d waiters on a pool of %d", response.StatusCode, 4*waiters, waiters)
		}
		var onRow int
		if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM workspaces%'`).Scan(&onRow); err != nil {
			t.Fatal(err)
		}
		if onRow > 0 {
			t.Fatalf("%d connections are waiting on the workspace row", onRow)
		}
	}

	unlock()
	if err := <-exported; err != nil {
		t.Fatal(err)
	}
	for range 4 * waiters {
		select {
		case <-finished:
		case <-ctx.Done():
			t.Fatal("a queued writer never ran after the lock was released")
		}
	}
	var name string
	var joined bool
	if err := f.st.Pool.QueryRow(ctx, "SELECT name,EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND account_id=$2) FROM workspaces WHERE id=$1", ws, removed.ID).Scan(&name, &joined); err != nil || name != "Renamed" || !joined {
		t.Fatalf("queued writes were not applied: name=%q joined=%v err=%v", name, joined, err)
	}
}

// The membership read before the queue can be stale by the time the lock is held.
func TestMemberRemovedWhileWaitingForWorkspaceLockCannotWrite(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	owner := f.browser(t)
	ws := owner.login("owner@example.test", f.password).Workspaces[0].Id
	removing, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer removing.Rollback(context.Background())
	q := db.New(removing)
	if _, err = q.LockWorkspaceByID(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if err = q.DeleteMembership(ctx, db.DeleteMembershipParams{WorkspaceID: ws, AccountID: f.owner}); err != nil {
		t.Fatal(err)
	}
	status := make(chan int, 1)
	go func() {
		request, err := newRequest(owner, "PUT", "/api/v1/workspaces/"+ws.String(), []byte(`{"name":"Renamed","requireMfa":false}`))
		if err != nil {
			status <- 0
			return
		}
		response, err := owner.client.Do(request.WithContext(ctx))
		if err != nil {
			status <- 0
			return
		}
		response.Body.Close()
		status <- response.StatusCode
	}()
	for waiting := false; !waiting; time.Sleep(10 * time.Millisecond) {
		if err = f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM workspaces WHERE id=$1 FOR NO KEY UPDATE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	if err = removing.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-status; got != 404 {
		t.Fatalf("removed member's write status %d", got)
	}
	var name string
	if err = f.st.Pool.QueryRow(ctx, "SELECT name FROM workspaces WHERE id=$1", ws).Scan(&name); err != nil || name == "Renamed" {
		t.Fatalf("removed member's write committed: name=%q err=%v", name, err)
	}
}
