package httpapi

import (
	"sync"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// The start signal runs a shell in the container, so a side arriving as the first side gives
// up must never complete a pair whose other stream is already closing.
func TestTerminalLateSideNeverJoinsAnAbandonedPair(t *testing.T) {
	hub := newTerminalHub()
	stream := execution.TroubleshootingStream{Session: uuid.New()}
	agent, browser := &websocket.Conn{}, &websocket.Conn{}
	for range 20000 {
		first, complete := hub.join(stream, nil, browser)
		if complete {
			t.Fatal("one side completed a pair")
		}
		var (
			workers   sync.WaitGroup
			abandoned bool
			second    *terminalPair
		)
		start := make(chan struct{})
		workers.Go(func() {
			<-start
			abandoned = hub.abandon(first)
		})
		workers.Go(func() {
			<-start
			second, complete = hub.join(stream, agent, nil)
		})
		close(start)
		workers.Wait()
		if abandoned == complete {
			t.Fatalf("abandoned %v and completed %v", abandoned, complete)
		}
		if abandoned == (second == first) {
			t.Fatalf("abandoned %v, joined the first pair %v", abandoned, second == first)
		}
		hub.remove(stream.Session)
	}
}
