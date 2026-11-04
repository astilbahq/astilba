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

// Each account and workspace stops at its own limit without costing any other, every stream
// stops at the total, and a finished stream gives back each of its places.
func TestTerminalPendingLimits(t *testing.T) {
	hub := newTerminalHub()
	admit := func(account, workspace uuid.UUID) {
		t.Helper()
		if !hub.admitPending(account, workspace) {
			t.Fatalf("refused with %d held", hub.pendingTotal)
		}
	}
	greedy, tenant, other := uuid.New(), uuid.New(), uuid.New()
	for range terminalPendingPerAccount {
		admit(greedy, tenant)
	}
	if hub.admitPending(greedy, other) {
		t.Fatal("admitted past the account's limit")
	}
	for range terminalPendingPerWorkspace - terminalPendingPerAccount {
		admit(uuid.New(), tenant)
	}
	member := uuid.New()
	if hub.admitPending(member, tenant) {
		t.Fatal("admitted past the workspace's limit")
	}

	// Neither refusal counted anywhere: the refused account keeps all its places, and the
	// account refused by its own limit took none in the workspace it named.
	for range terminalPendingPerAccount {
		admit(member, other)
	}
	if got := hub.pendingByWorkspace[other]; got != terminalPendingPerAccount {
		t.Fatalf("another workspace holds %d", got)
	}

	for hub.pendingTotal < terminalPendingTotal {
		admit(uuid.New(), uuid.New())
	}
	if hub.admitPending(uuid.New(), uuid.New()) {
		t.Fatal("admitted past the total")
	}
	hub.releasePending(greedy, tenant)
	admit(greedy, tenant)
}
