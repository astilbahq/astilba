package integration

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// Whatever a session depends on, its end reaches an open stream promptly, and the agent
// is told why.
func TestTroubleshootingOpenSessionsEndWhenConditionsChange(t *testing.T) {
	ctx := context.Background()
	for name, test := range map[string]struct {
		reason string
		change func(t *testing.T, x troubleshootingFixture, owner, operator *browser, session uuid.UUID, operatorID uuid.UUID)
	}{
		"closed by its owner": {"closed by owner", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, session, _ uuid.UUID) {
			owner.call("POST", x.prefix+"/troubleshooting-sessions/"+session.String()+"/close", nil, 204, nil)
		}},
		"role removed": {"role changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, operatorID uuid.UUID) {
			owner.call("DELETE", x.prefix+"/members/"+operatorID.String(), nil, 204, nil)
		}},
		"setting off": {"environment setting changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, _ uuid.UUID) {
			owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: false, Generation: 1}, 200, nil)
		}},
		"browser session idled out": {"browser session ended", func(t *testing.T, x troubleshootingFixture, _, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, browserSessionExpiry("now()-interval '1 second'"), session); err != nil {
				t.Fatal(err)
			}
		}},
		"logged out": {"browser session ended", func(t *testing.T, x troubleshootingFixture, _, operator *browser, _, _ uuid.UUID) {
			operator.call("POST", "/api/v1/auth/logout", nil, 204, nil)
		}},
		"password changed": {"account security changed", func(t *testing.T, x troubleshootingFixture, _, _ *browser, _, operatorID uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE accounts SET auth_version=auth_version+1 WHERE id=$1", operatorID); err != nil {
				t.Fatal(err)
			}
		}},
		"server revoked": {"server credential changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE servers SET revoked=true WHERE id=(SELECT server_id FROM troubleshooting_sessions WHERE id=$1)", session); err != nil {
				t.Fatal(err)
			}
		}},
		"credential rotated": {"server credential changed", func(t *testing.T, x troubleshootingFixture, _, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE servers SET credential_hash=sha256('rotated') WHERE id=(SELECT server_id FROM troubleshooting_sessions WHERE id=$1)", session); err != nil {
				t.Fatal(err)
			}
		}},
		"controller replaced": {"server credential changed", func(t *testing.T, x troubleshootingFixture, _, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE servers SET controller_generation=controller_generation+1 WHERE id=(SELECT server_id FROM troubleshooting_sessions WHERE id=$1)", session); err != nil {
				t.Fatal(err)
			}
		}},
		"workspace now requires MFA": {"multi-factor authentication required", func(t *testing.T, x troubleshootingFixture, _, _ *browser, _, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE workspaces SET require_mfa=true WHERE id=$1", x.ws); err != nil {
				t.Fatal(err)
			}
		}},
		"demoted under an owners-only restriction": {"environment setting changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, operatorID uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_policies SET owners_only=true WHERE environment_id=$1", x.env); err != nil {
				t.Fatal(err)
			}
		}},
		"closed with all the others": {"closed by owner", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, _ uuid.UUID) {
			owner.call("POST", x.prefix+"/troubleshooting-sessions/close", nil, 204, nil)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			x := relayFixture(t)
			operator := x.member(t, "operator@example.test", "operator")
			var operatorID uuid.UUID
			if err := x.f.st.Pool.QueryRow(ctx, "SELECT id FROM accounts WHERE email='operator@example.test'").Scan(&operatorID); err != nil {
				t.Fatal(err)
			}
			opened, agent, browser := x.connected(t, operator, 1)
			test.change(t, x, x.owner, operator, opened.Session.Id, operatorID)
			x.waitClosed(t, opened.Session.Id, test.reason)
			agent.toldWhy(t, test.reason)
			browser.drained(t)
		})
	}
}

// A session closed elsewhere keeps that reason, and the agent is told the reason kept, so
// the host's journal agrees with management's record even when the relay ends for another
// reason before it has read the close.
func TestTroubleshootingAgentHearsTheReasonTheSessionKeeps(t *testing.T) {
	ctx := context.Background()
	closeSession := func(x troubleshootingFixture, session uuid.UUID) {
		x.owner.call("POST", x.prefix+"/troubleshooting-sessions/"+session.String()+"/close", nil, 204, nil)
	}
	t.Run("running", func(t *testing.T) {
		x := relayFixture(t)
		x.f.apiServer.TerminalReviewEvery = time.Hour // the relay never reads the close
		opened, agent, browser := x.connected(t, x.owner, 1)
		closeSession(x, opened.Session.Id)
		browser.conn.CloseNow()
		agent.toldWhy(t, "closed")
		x.waitClosed(t, opened.Session.Id, "closed")
	})
	t.Run("never attached", func(t *testing.T) {
		x := relayFixture(t)
		opened := x.open(t, x.owner, x.app, 1, 201)
		if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_sessions SET ticket_expires_at=now()+interval '2 seconds' WHERE id=$1", opened.Session.Id); err != nil {
			t.Fatal(err)
		}
		agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
		if agent == nil {
			t.Fatalf("agent stream refused: %d", status)
		}
		closeSession(x, opened.Session.Id)
		agent.toldWhy(t, "closed")
		x.waitClosed(t, opened.Session.Id, "closed")
	})
}
