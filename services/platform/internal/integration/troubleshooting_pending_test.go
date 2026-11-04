package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
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
