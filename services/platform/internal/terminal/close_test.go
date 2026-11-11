package terminal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// closedWith closes a real stream from the server's side and returns what the client's
// read reports.
func closedWith(t *testing.T, end func(*websocket.Conn)) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		end(conn)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, _, err = conn.Read(ctx)
	return err
}

func TestEndingReadsWhyThePeerClosed(t *testing.T) {
	long := strings.Repeat("x", 200)
	for name, test := range map[string]struct {
		end    func(*websocket.Conn)
		reason string
		closed bool
	}{
		"with a reason": {func(c *websocket.Conn) { Close(c, websocket.StatusNormalClosure, "idle timeout") }, "idle timeout", true},
		"without one":   {func(c *websocket.Conn) { Close(c, websocket.StatusNormalClosure, "") }, "", true},
		// The library would drop the connection, sending no close at all, for a reason
		// longer than a close frame holds.
		"too long":       {func(c *websocket.Conn) { Close(c, websocket.StatusNormalClosure, long) }, long[:maxCloseReason], true},
		"not printable":  {func(c *websocket.Conn) { Close(c, websocket.StatusNormalClosure, "idle\ntimeout") }, "", true},
		"never attached": {func(c *websocket.Conn) { Close(c, websocket.StatusPolicyViolation, "not attached") }, "not attached", true},
		// Caddy says this to both sides as it reloads: management ended nothing.
		"a proxy going away": {func(c *websocket.Conn) { Close(c, websocket.StatusGoingAway, "") }, "", false},
		"no status":          {func(c *websocket.Conn) { Close(c, websocket.StatusNoStatusRcvd, "") }, "", false},
		"dropped":            {func(c *websocket.Conn) { c.CloseNow() }, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := closedWith(t, test.end)
			if reason, closed := Ending(err); reason != test.reason || closed != test.closed {
				t.Fatalf("ending %q closed=%v from %v, want %q closed=%v", reason, closed, err, test.reason, test.closed)
			}
		})
	}
	for _, err := range []error{nil, context.Canceled, fmt.Errorf("read: %w", errors.New("reset"))} {
		if reason, closed := Ending(err); reason != "" || closed {
			t.Fatalf("%v read as a close for %q", err, reason)
		}
	}
}
