package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Streams without a redeemed ticket hold sockets outside every session limit. Past the
// account's limit an upgrade is refused before it happens, without affecting anyone else,
// and neither a redemption nor a refusal keeps the place.
func TestTroubleshootingUnredeemedStreamsAreLimited(t *testing.T) {
	x := relayFixture(t)
	operator := x.member(t, "operator@example.test", "operator")
	opened := x.open(t, x.owner, x.app, 1, 201)
	dial := func(b *browser) (*websocket.Conn, int) {
		return x.dialBrowser(t, b, opened.Session.Id, x.f.server.URL)
	}
	// Retries a place that is on its way back. Every call shares a deadline shorter than the
	// five-second ticket wait, whose ending would free a place regardless.
	deadline := time.Now().Add(4 * time.Second)
	hold := func(want int) []*websocket.Conn {
		t.Helper()
		var held []*websocket.Conn
		for ; len(held) < want; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("holding %d streams, want %d", len(held), want)
			}
			if conn, _ := dial(x.owner); conn != nil {
				held = append(held, conn)
			}
		}
		return held
	}

	redeemed := hold(1)[0]
	if err := redeemed.Write(context.Background(), websocket.MessageText, []byte(opened.Ticket)); err != nil {
		t.Fatal(err)
	}
	readTerminal(redeemed)
	held := hold(4)
	if conn, status := dial(x.owner); conn != nil || status != http.StatusTooManyRequests {
		t.Fatalf("stream past the limit: %d", status)
	}
	if conn, status := dial(operator); conn == nil {
		t.Fatalf("another account refused: %d", status)
	}

	if err := held[0].Write(context.Background(), websocket.MessageText, []byte("wrong-ticket")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := held[0].Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("refused stream: %v", err)
	}
	hold(1)

	// The redeemed stream outlived its place.
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	agent.expect(t, []byte{terminal.Start})
}

// The path can name any workspace. Its operators stop at the workspace's limit, and accounts
// that could never redeem a ticket are refused without taking a place, even once it is full.
func TestTroubleshootingWorkspaceLimitsUnredeemedStreams(t *testing.T) {
	x := relayFixture(t)
	second, third := x.member(t, "second@example.test", "operator"), x.member(t, "third@example.test", "operator")
	viewer := x.member(t, "viewer@example.test", "viewer")
	former := x.member(t, "former@example.test", "operator")
	ctx := context.Background()
	account, err := x.f.st.Queries.AccountByEmail(ctx, "former@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.f.st.Queries.DeleteMembership(ctx, db.DeleteMembershipParams{WorkspaceID: x.ws, AccountID: account.ID}); err != nil {
		t.Fatal(err)
	}
	session := uuid.New()
	dial := func(b *browser) (*websocket.Conn, int) {
		return x.dialBrowser(t, b, session, x.f.server.URL)
	}
	refused := func() {
		t.Helper()
		if conn, status := dial(former); conn != nil || status != http.StatusNotFound {
			t.Fatalf("stream naming another workspace: %d", status)
		}
		if conn, status := dial(viewer); conn != nil || status != http.StatusForbidden {
			t.Fatalf("read-only member's stream: %d", status)
		}
	}
	// More than either limit would allow if these streams counted.
	for range 9 {
		refused()
	}
	// Every place must be held at once, and the five-second ticket wait ending frees one.
	started := time.Now()
	for _, b := range []*browser{x.owner, second} {
		for range 4 {
			if conn, status := dial(b); conn == nil {
				t.Fatalf("member refused within the limit: %d", status)
			}
		}
	}
	conn, status := dial(third)
	if time.Since(started) > 4*time.Second {
		t.Fatal("too slow to hold every place at once")
	}
	if conn != nil || status != http.StatusTooManyRequests {
		t.Fatalf("stream past the workspace's limit: %d", status)
	}
	refused()
}
