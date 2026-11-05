package httpapi

import (
	"sync"

	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Limits on browser streams that have not yet redeemed a ticket. These are starting values,
// not measurements: a person needs one such stream per session, briefly, and each one held
// costs a socket and a goroutine for at most about 20 seconds.
const (
	terminalPendingPerAccount   = 4
	terminalPendingPerWorkspace = 8
	terminalPendingTotal        = 64
)

// terminalPair joins the two streams of one session. Whichever handler arrives second
// runs the relay; the first waits for it, so both connections stay owned by a handler.
type terminalPair struct {
	agent, browser *websocket.Conn
	stream         execution.TroubleshootingStream
	ready          chan struct{}
	done           chan struct{}
	// earlyFrames holds what a browser sends while waiting for its agent, so that those
	// reads can notice it leaving. It closes when they stop, and the pump drains it first.
	earlyFrames chan terminalRead
	browserLeft chan struct{}
}

type terminalRead struct {
	kind  websocket.MessageType
	frame []byte
	err   error
}

// terminalHub holds the sessions of this single management instance.
//
// It also counts browser streams between their upgrade and their ticket's outcome, which a
// signed-in client can open without any session limit applying. Hijacked streams escape the
// server's timeouts, so an uncounted loop could run out of file descriptors. The per-account
// count stops one account using up the total and blocking everyone else. A workspace owner
// can invite any number of accounts, so the per-workspace count does the same for a tenant:
// eight workspaces, rather than one, are needed to reach the total.
type terminalHub struct {
	mu                 sync.Mutex
	sessions           map[uuid.UUID]*terminalPair
	pendingByAccount   map[uuid.UUID]int
	pendingByWorkspace map[uuid.UUID]int
	pendingTotal       int
}

func newTerminalHub() *terminalHub {
	return &terminalHub{sessions: map[uuid.UUID]*terminalPair{}, pendingByAccount: map[uuid.UUID]int{}, pendingByWorkspace: map[uuid.UUID]int{}}
}

// admitPending checks every limit before counting anything, so a refusal takes no place from
// the account, its workspace or the total.
func (h *terminalHub) admitPending(account, workspace uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pendingTotal >= terminalPendingTotal || h.pendingByAccount[account] >= terminalPendingPerAccount || h.pendingByWorkspace[workspace] >= terminalPendingPerWorkspace {
		return false
	}
	h.pendingTotal++
	h.pendingByAccount[account]++
	h.pendingByWorkspace[workspace]++
	return true
}

func (h *terminalHub) releasePending(account, workspace uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingTotal--
	if h.pendingByAccount[account]--; h.pendingByAccount[account] == 0 {
		delete(h.pendingByAccount, account)
	}
	if h.pendingByWorkspace[workspace]--; h.pendingByWorkspace[workspace] == 0 {
		delete(h.pendingByWorkspace, workspace)
	}
}

// join registers one side and reports whether it completes the pair. Finding the pair and
// taking a place in it are one step, so a side arriving as abandon runs either completes
// the pair first or gets a new one of its own, never one that is already closing.
func (h *terminalHub) join(stream execution.TroubleshootingStream, agent, browser *websocket.Conn) (*terminalPair, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.sessions[stream.Session]
	if p == nil {
		p = &terminalPair{stream: stream, ready: make(chan struct{}), done: make(chan struct{})}
		h.sessions[stream.Session] = p
	}
	if agent != nil {
		p.agent = agent
	}
	if browser != nil {
		p.browser = browser
	}
	complete := p.agent != nil && p.browser != nil
	if complete {
		close(p.ready)
	} else if browser != nil {
		// Set here so the relay finds them, whenever the other side joins.
		p.earlyFrames, p.browserLeft = make(chan terminalRead, terminalEarlyFrames), make(chan struct{})
	}
	return p, complete
}

// abandon gives up a pair the other side has not joined, and reports whether it did.
func (h *terminalHub) abandon(p *terminalPair) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p.agent != nil && p.browser != nil {
		return false
	}
	delete(h.sessions, p.stream.Session)
	return true
}

func (h *terminalHub) remove(id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
}
