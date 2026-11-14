package integration

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// The Workspace page and the recovery_window alert both judge the newest recovery base's window,
// so a base without one, a short one and a longer one each show the same way in both.
func TestRecoveryWindowAlertAndWorkspaceView(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	route := "/api/v1/workspaces/" + ws.String() + "/cross-host-recovery"
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := func(captured time.Time, window *time.Time) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, "INSERT INTO recovery_bases(workspace_id,id,parent_id,environment_id,primary_server_id,captured_at,window_start) VALUES($1,$2,$3,$4,$5,$6,$7)", ws, uuid.New(), uuid.New(), uuid.New(), uuid.New(), captured, window); err != nil {
			t.Fatal(err)
		}
	}
	view := func(captured time.Time, window *time.Time, short bool) {
		t.Helper()
		var got api.CrossHostRecovery
		b.call("GET", route, nil, 200, &got)
		if got.CheckpointAt == nil || !got.CheckpointAt.Equal(captured) || (got.WindowStart == nil) != (window == nil) || window != nil && !got.WindowStart.Equal(*window) || got.ShortWindow != short {
			t.Fatalf("cross-host recovery is %+v, not checkpoint %v, window %v, short %v", got, captured, window, short)
		}
	}
	alerts := func(state string, want int) {
		t.Helper()
		var got int
		if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE workspace_id=$1 AND kind='recovery_window' AND resource_id=$1 AND server_id IS NULL AND environment_id IS NULL AND state=$2", ws, state).Scan(&got); err != nil || got != want {
			t.Fatalf("%s recovery_window alerts=%d want=%d: %v", state, got, want, err)
		}
	}

	var none api.CrossHostRecovery
	b.call("GET", route, nil, 200, &none)
	if none.CheckpointAt != nil || none.WindowStart != nil || none.ShortWindow {
		t.Fatalf("a workspace without a checkpoint shows %+v", none)
	}

	// A base captured before recovery windows has none, which isn't short.
	old := now.Add(-48 * time.Hour)
	base(old, nil)
	view(old, nil, false)
	alertSweep(t, f, ws)
	alerts("firing", 0)

	short, shortStart := now.Add(-24*time.Hour), now.Add(-21*24*time.Hour)
	base(short, &shortStart)
	view(short, &shortStart, true)
	alertSweep(t, f, ws)
	alerts("firing", 1)
	var inbox api.AlertInbox
	b.call("GET", "/api/v1/workspaces/"+ws.String()+"/alerts?view=attention", nil, 200, &inbox)
	if len(inbox.Alerts) != 1 || inbox.Alerts[0].Kind != api.RecoveryWindow || inbox.Alerts[0].ResourceId != ws {
		t.Fatalf("the inbox does not show the short window: %+v", inbox)
	}

	// Owners and operators read the window; viewers see only the alert, and other accounts nothing.
	hash, _ := f.auth.Passwords.Hash(f.password)
	for _, member := range []struct {
		email, role string
		status      int
	}{{"window-operator@example.test", "operator", 200}, {"window-viewer@example.test", "viewer", 403}, {"window-outsider@example.test", "", 404}} {
		id := uuid.New()
		if _, err := f.st.Pool.Exec(ctx, "INSERT INTO accounts(id,email,name,password_hash) VALUES($1,$2,'Member',$3)", id, member.email, hash); err != nil {
			t.Fatal(err)
		}
		if member.role != "" {
			if _, err := f.st.Pool.Exec(ctx, "INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,$3)", ws, id, member.role); err != nil {
				t.Fatal(err)
			}
		}
		c := f.browser(t)
		c.login(member.email, f.password)
		c.call("GET", route, nil, member.status, nil)
	}

	longStart := now.Add(-90 * 24 * time.Hour)
	base(now, &longStart)
	view(now, &longStart, false)
	alertSweep(t, f, ws)
	alerts("firing", 0)
	alerts("resolved", 1)
}
