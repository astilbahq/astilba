package httpapi

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

const (
	terminalTicketWait  = 5 * time.Second
	terminalPing        = 30 * time.Second
	terminalWrite       = 30 * time.Second
	terminalInputRate   = 64 << 10
	terminalOutputRate  = 1 << 20
	terminalResizeRate  = 10
	terminalResizeBurst = 20
)

// terminalPair joins the two streams of one session. Whichever handler arrives second
// runs the relay; the first waits for it, so both connections stay owned by a handler.
type terminalPair struct {
	agent, browser *websocket.Conn
	stream         execution.TroubleshootingStream
	ready          chan struct{}
	done           chan struct{}
}

// terminalHub holds the sessions of this single management instance.
type terminalHub struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]*terminalPair
}

func newTerminalHub() *terminalHub { return &terminalHub{sessions: map[uuid.UUID]*terminalPair{}} }

func (h *terminalHub) pair(id uuid.UUID, stream execution.TroubleshootingStream) *terminalPair {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.sessions[id]
	if p == nil {
		p = &terminalPair{stream: stream, ready: make(chan struct{}), done: make(chan struct{})}
		h.sessions[id] = p
	}
	return p
}

func (h *terminalHub) remove(id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
}

// acceptTerminal upgrades a checked request. Hijacking clears the server's ordinary
// deadlines, which do not suit a connection that lasts up to an hour.
func acceptTerminal(w http.ResponseWriter, r *http.Request, options *websocket.AcceptOptions) (*websocket.Conn, error) {
	conn, err := websocket.Accept(w, r, options)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(terminal.MaxFrame)
	return conn, nil
}

// writeTerminal bounds every write: a peer that stops reading loses its stream rather
// than blocking the relay, its limits and its re-checks.
func writeTerminal(ctx context.Context, conn *websocket.Conn, frame []byte) error {
	ctx, cancel := context.WithTimeout(ctx, terminalWrite)
	defer cancel()
	return conn.Write(ctx, websocket.MessageBinary, frame)
}

func (s *Server) agentTerminal(w http.ResponseWriter, r *http.Request) {
	identity, err := s.agentRequestIdentity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "sessionId"))
	if err != nil {
		s.fail(w, r, domain.NotFound)
		return
	}
	stream, err := s.Execution.AdmitTroubleshootingAgent(r.Context(), identity, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	conn, err := acceptTerminal(w, r, nil)
	if err != nil {
		s.endUnattachedTerminal(stream, "agent stream failed")
		return
	}
	s.attachTerminal(stream, conn, nil)
}

func (s *Server) browserTerminal(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.Origin {
		s.fail(w, r, domain.Forbidden)
		return
	}
	ws, wsErr := uuid.Parse(chi.URLParam(r, "workspaceId"))
	id, idErr := uuid.Parse(chi.URLParam(r, "sessionId"))
	if wsErr != nil || idErr != nil {
		s.fail(w, r, domain.NotFound)
		return
	}
	cookie, err := r.Cookie(s.Sessions.Cookie.Name)
	if err != nil {
		s.fail(w, r, domain.Unauthenticated)
		return
	}
	// The session is only read: a hijacked response has no chance to save it.
	ctx, err := s.Sessions.Load(context.WithValue(r.Context(), sessionLoadKey{}, &sessionLoad{}), cookie.Value)
	if err != nil {
		s.fail(w, r, domain.Unauthenticated)
		return
	}
	p := s.browserPrincipal(ctx)
	if p == nil {
		s.fail(w, r, domain.Unauthenticated)
		return
	}
	// The Origin was compared exactly above. The library's own check compares it with
	// the Host header, which a reverse proxy may rewrite.
	conn, err := acceptTerminal(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	first, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalTicketWait)
	_, ticket, err := conn.Read(first)
	cancel()
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "ticket required")
		return
	}
	stream, err := s.Execution.RedeemTroubleshootingTicket(context.WithoutCancel(ctx), *p, execution.BrowserBinding{Token: cookie.Value}, ws, id, string(ticket))
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "session refused")
		return
	}
	s.attachTerminal(stream, nil, conn)
}

func (s *Server) endUnattachedTerminal(stream execution.TroubleshootingStream, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Execution.EndTroubleshootingSession(ctx, stream.Workspace, stream.Session, reason, 0, 0); err != nil {
		s.Log.Error("terminal session end failed", "error", err)
	}
}

// attachTerminal registers one side. The second side to arrive runs the relay; the
// first waits, and ends the session if the other never arrives before the ticket expires.
func (s *Server) attachTerminal(stream execution.TroubleshootingStream, agent, browser *websocket.Conn) {
	p := s.terminals.pair(stream.Session, stream)
	s.terminals.mu.Lock()
	if agent != nil {
		p.agent = agent
	}
	if browser != nil {
		p.browser = browser
	}
	complete := p.agent != nil && p.browser != nil
	if complete {
		close(p.ready)
	}
	s.terminals.mu.Unlock()
	if complete {
		defer close(p.done)
		defer s.terminals.remove(stream.Session)
		s.relayTerminal(p)
		return
	}
	select {
	case <-p.ready:
		<-p.done
	case <-time.After(time.Until(stream.TicketExpires)):
		s.terminals.mu.Lock()
		unpaired := p.agent == nil || p.browser == nil
		if unpaired {
			delete(s.terminals.sessions, stream.Session)
		}
		s.terminals.mu.Unlock()
		if !unpaired {
			<-p.done
			return
		}
		for _, conn := range []*websocket.Conn{agent, browser} {
			if conn != nil {
				conn.Close(websocket.StatusPolicyViolation, "the other side did not attach")
			}
		}
		s.endUnattachedTerminal(stream, "not attached")
	}
}

// relayTerminal runs until the first reason to end. Terminal content is forwarded but
// never logged; only byte counts are kept.
func (s *Server) relayTerminal(p *terminalPair) {
	ctx, cancel := context.WithDeadline(context.Background(), p.stream.Deadline)
	defer cancel()
	var once sync.Once
	reason := "deadline"
	stop := func(why string) {
		if ctx.Err() != nil {
			return // the streams are closing because of the reason already recorded
		}
		once.Do(func() {
			reason = why
			cancel()
		})
	}
	var input, output atomic.Int64
	var lastInput atomic.Int64
	lastInput.Store(time.Now().UnixNano())
	ws, id := p.stream.Workspace, p.stream.Session

	if err := s.Execution.ActivateTroubleshootingSession(ctx, ws, id); err != nil {
		stop("activation failed")
	} else if err = writeTerminal(ctx, p.agent, []byte{terminal.Start}); err != nil {
		stop("agent stream lost")
	}

	var workers sync.WaitGroup
	workers.Go(func() { s.pumpTerminalBrowser(ctx, p, &input, &lastInput, stop) })
	workers.Go(func() { s.pumpTerminalAgent(ctx, p, &output, stop) })
	workers.Go(func() { s.superviseTerminal(ctx, p, &lastInput, stop) })
	<-ctx.Done()
	once.Do(func() {})
	p.agent.Close(websocket.StatusNormalClosure, "")
	p.browser.Close(websocket.StatusNormalClosure, "")
	workers.Wait()

	end, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	if err := s.Execution.EndTroubleshootingSession(end, ws, id, reason, input.Load(), output.Load()); err != nil {
		s.Log.Error("terminal session end failed", "error", err)
	}
}

func (s *Server) pumpTerminalBrowser(ctx context.Context, p *terminalPair, input, lastInput *atomic.Int64, stop func(string)) {
	// Waiting on a limit stops this read, which is the backpressure: nothing is buffered.
	inputRate := rate.NewLimiter(terminalInputRate, terminal.MaxFrame)
	resizeRate := rate.NewLimiter(terminalResizeRate, terminalResizeBurst)
	lastRefresh := time.Time{}
	for {
		kind, frame, err := p.browser.Read(ctx)
		if err != nil {
			stop("browser stream lost")
			return
		}
		if kind != websocket.MessageBinary || len(frame) == 0 {
			stop("invalid frame")
			return
		}
		switch frame[0] {
		case terminal.Input:
			input.Add(int64(len(frame) - 1))
			now := time.Now()
			lastInput.Store(now.UnixNano())
			if now.Sub(lastRefresh) >= time.Minute {
				lastRefresh = now
				if err = s.Execution.RefreshTroubleshootingBrowser(ctx, p.stream.Workspace, p.stream.Session, s.Sessions.IdleTimeout, p.stream.Deadline); err != nil && ctx.Err() == nil {
					s.Log.Error("terminal browser refresh failed", "error", err)
				}
			}
			if inputRate.WaitN(ctx, len(frame)) != nil {
				return // the wait would pass the deadline, which ends the session
			}
		case terminal.Resize:
			if _, _, err = terminal.ParseResize(frame); err != nil {
				stop("invalid frame")
				return
			}
			if resizeRate.Wait(ctx) != nil {
				return
			}
		default:
			stop("invalid frame")
			return
		}
		if err = writeTerminal(ctx, p.agent, frame); err != nil {
			stop("agent stream lost")
			return
		}
	}
}

func (s *Server) pumpTerminalAgent(ctx context.Context, p *terminalPair, output *atomic.Int64, stop func(string)) {
	outputRate := rate.NewLimiter(terminalOutputRate, terminal.MaxFrame)
	for {
		kind, frame, err := p.agent.Read(ctx)
		if err != nil {
			stop("agent stream lost")
			return
		}
		if kind != websocket.MessageBinary || len(frame) == 0 {
			stop("invalid frame")
			return
		}
		ending := ""
		switch frame[0] {
		case terminal.Output:
			output.Add(int64(len(frame) - 1))
			if outputRate.WaitN(ctx, len(frame)) != nil {
				return // the wait would pass the deadline, which ends the session
			}
		case terminal.Exit:
			ending = "shell exited"
		case terminal.Refused:
			why := terminal.Refusal(frame[1:])
			frame, ending = append([]byte{terminal.Refused}, why...), "agent refused: "+why
		default:
			stop("invalid frame")
			return
		}
		if err = writeTerminal(ctx, p.browser, frame); err != nil {
			stop("browser stream lost")
			return
		}
		if ending != "" {
			stop(ending)
			return
		}
	}
}

// superviseTerminal enforces the idle limit, warns before either limit, keeps both
// connections alive and re-reads the conditions a session depends on.
func (s *Server) superviseTerminal(ctx context.Context, p *terminalPair, lastInput *atomic.Int64, stop func(string)) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	nextReview, nextPing := time.Now().Add(s.TerminalReviewEvery), time.Now().Add(terminalPing)
	warned := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			activity := time.Unix(0, lastInput.Load())
			if now.Sub(activity) >= s.TerminalIdle {
				stop("idle timeout")
				return
			}
			end := activity.Add(s.TerminalIdle)
			if p.stream.Deadline.Before(end) {
				end = p.stream.Deadline
			}
			if time.Until(end) <= time.Minute && warned.Before(end) {
				warned = end
				if err := writeTerminal(ctx, p.browser, terminal.WarningFrame(uint16(max(time.Until(end).Seconds(), 0)))); err != nil {
					stop("browser stream lost")
					return
				}
			}
			if now.After(nextReview) {
				nextReview = now.Add(s.TerminalReviewEvery)
				why, err := s.Execution.ReviewTroubleshootingSession(ctx, p.stream.Workspace, p.stream.Session)
				if err != nil && ctx.Err() == nil {
					s.Log.Error("terminal session review failed", "error", err)
				}
				if why != "" {
					stop(why)
					return
				}
			}
			if now.After(nextPing) {
				nextPing = now.Add(terminalPing)
				for _, conn := range []*websocket.Conn{p.browser, p.agent} {
					ping, cancel := context.WithTimeout(ctx, 10*time.Second)
					err := conn.Ping(ping)
					cancel()
					if err != nil && ctx.Err() == nil {
						stop("stream lost")
						return
					}
				}
			}
		}
	}
}

// browserPrincipal reads the account a loaded browser session authenticates, or nil.
func (s *Server) browserPrincipal(ctx context.Context) *store.Principal {
	id, err := uuid.Parse(s.Sessions.GetString(ctx, "account"))
	if err != nil {
		return nil
	}
	a, err := s.Store.Queries.AccountByID(ctx, id)
	if err != nil || a.AuthVersion != s.Sessions.GetInt64(ctx, "version") {
		return nil
	}
	return &store.Principal{Account: a, MFA: s.Sessions.GetBool(ctx, "mfa"), Recent: store.Recent(s.Sessions.GetTime(ctx, "recent"), s.Auth.Now())}
}
func (s *Server) terminalAvailable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !s.Execution.Troubleshooting {
			s.fail(w, r, domain.TroubleshootingRefused(domain.TroubleshootingInstallation))
			return
		}
		next.ServeHTTP(w, r)
	})
}
