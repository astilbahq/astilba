package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/diagnostic"
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
	terminalRedeem      = 5 * time.Second
	terminalPing        = 30 * time.Second
	terminalWrite       = 30 * time.Second
	terminalQuery       = 5 * time.Second // bounds the relay's database calls, so a stall cannot hold it up
	terminalEndRetry    = 30 * time.Second
	terminalReviewLimit = 6 // review intervals without an answer before a session ends
	terminalEarlyFrames = 8 // browser frames held while watching for it to leave
	terminalInputRate   = 64 << 10
	terminalOutputRate  = 1 << 20
	terminalResizeRate  = 10
	terminalResizeBurst = 20
)

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
func writeTerminal(conn *websocket.Conn, frame []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), terminalWrite)
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
		s.fail(w, r, err)
		return
	}
	p, err := s.browserPrincipal(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The path can name any workspace, so only a member's stream may count against one.
	// Otherwise outsiders could fill another tenant's places, or avoid the workspace limit
	// by naming a different workspace for each stream. A read-only member can never redeem
	// a ticket, so its streams would only keep the workspace's operators out.
	member, err := s.Store.Queries.Workspace(ctx, db.WorkspaceParams{ID: ws, AccountID: p.Account.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !domain.CanWrite(member.Role) {
		s.fail(w, r, domain.Forbidden)
		return
	}
	// Checked before upgrading, so that a refused request costs no more than any other. The
	// place is held until redemption finishes or its refusal has closed.
	if !s.terminals.admitPending(p.Account.ID, ws) {
		s.fail(w, r, domain.RateLimited)
		return
	}
	release := sync.OnceFunc(func() { s.terminals.releasePending(p.Account.ID, ws) })
	defer release()
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
		terminal.Close(conn, websocket.StatusPolicyViolation, "ticket required")
		return
	}
	// Nothing reads the connection during this wait, so a busy pool or a held workspace
	// lock would otherwise keep the socket and this handler for as long as it lasts.
	redeem, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalRedeem)
	stream, err := s.Execution.RedeemTroubleshootingTicket(redeem, *p, execution.BrowserBinding{Token: cookie.Value}, ws, id, string(ticket))
	cancel()
	if err != nil {
		var refused *domain.Error
		if !errors.As(err, &refused) {
			s.Log.Error("terminal ticket redemption failed", diagnostic.Failure(err))
		}
		terminal.Close(conn, websocket.StatusPolicyViolation, "session refused")
		return
	}
	release()
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
// first waits, and ends the session if the other never arrives before the ticket expires
// or if a waiting browser leaves, so that no shell starts for nobody.
func (s *Server) attachTerminal(stream execution.TroubleshootingStream, agent, browser *websocket.Conn) {
	p, complete := s.terminals.join(stream, agent, browser)
	if !complete && browser != nil {
		go watchTerminalBrowser(browser, p.earlyFrames, p.browserLeft)
	}
	if complete {
		defer close(p.done)
		defer s.terminals.remove(stream.Session)
		s.relayTerminal(p)
		return
	}
	reason := "not attached"
	select {
	case <-p.ready:
		<-p.done
		return
	case <-time.After(time.Until(stream.TicketExpires)):
	case <-p.browserLeft:
		reason = "browser stream lost"
	}
	if !s.terminals.abandon(p) {
		<-p.done
		return
	}
	// The agent is told the reason the session records, for the host's journal.
	if agent != nil {
		terminal.Close(agent, websocket.StatusPolicyViolation, reason)
	}
	if browser != nil {
		terminal.Close(browser, websocket.StatusPolicyViolation, "the other side did not attach")
	}
	s.endUnattachedTerminal(stream, reason)
}

// watchTerminalBrowser reads a browser that is waiting for its agent. Frames wait for the
// pump, and past the channel's capacity it stops, which leaves the pump's own backpressure.
func watchTerminalBrowser(conn *websocket.Conn, frames chan<- terminalRead, left chan<- struct{}) {
	defer close(frames)
	for range cap(frames) {
		kind, frame, err := conn.Read(context.Background())
		frames <- terminalRead{kind, frame, err}
		if err != nil {
			close(left)
			return
		}
	}
}

// relayTerminal runs until the first reason to end. Terminal content is forwarded but
// never logged; only byte counts are kept.
//
// The session's context never reaches a connection operation: the library drops the
// connection when one is cancelled, and closing over unread input resets it, which can
// destroy the frame just written. Each stream ends with terminal.Close instead, which also
// ends its blocked read.
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
	var refreshDue atomic.Bool
	activity := terminal.NewActivity()
	ws, id := p.stream.Workspace, p.stream.Session

	// The first stream may have waited up to a ticket's lifetime, so the conditions are
	// read again before anything can start.
	query, finish := context.WithTimeout(ctx, terminalQuery)
	why, err := s.Execution.ReviewTroubleshootingSession(query, ws, id)
	if err == nil && why == "" {
		err = s.Execution.ActivateTroubleshootingSession(query, ws, id)
	}
	finish()
	if why != "" {
		stop(why)
	} else if err != nil {
		s.Log.Error("terminal session activation failed", "error", err)
		stop("activation failed")
	} else if err = writeTerminal(p.agent, []byte{terminal.Start}); err != nil {
		stop("agent stream lost")
	}

	var workers sync.WaitGroup
	workers.Go(func() { s.pumpTerminalBrowser(ctx, p, &input, activity, &refreshDue, stop) })
	workers.Go(func() { s.pumpTerminalAgent(ctx, p, &output, stop) })
	workers.Go(func() { s.superviseTerminal(ctx, p, activity, &refreshDue, stop) })
	<-ctx.Done()
	once.Do(func() {})
	// Concurrently, so a peer that does not answer cannot delay the other stream's end. The
	// agent is told the reason the session records, so the host's journal can say why
	// management ended it.
	workers.Go(func() { terminal.Close(p.agent, websocket.StatusNormalClosure, reason) })
	workers.Go(func() { terminal.Close(p.browser, websocket.StatusNormalClosure, "") })
	workers.Wait()

	s.recordTerminalEnd(p.stream, reason, input.Load(), output.Load())
}

// recordTerminalEnd retries, because no relay remains to close the row: left open, it
// would hold its limits and lose its byte counts. A minute past the deadline the sweep
// closes it, so retrying stops there.
func (s *Server) recordTerminalEnd(stream execution.TroubleshootingStream, reason string, input, output int64) {
	for wait := time.Second; ; wait = min(wait*2, terminalEndRetry) {
		ctx, cancel := context.WithTimeout(context.Background(), terminalQuery)
		err := s.Execution.EndTroubleshootingSession(ctx, stream.Workspace, stream.Session, reason, input, output)
		cancel()
		if err == nil {
			return
		}
		s.Log.Error("terminal session end failed", "error", err)
		if time.Now().Add(wait).After(stream.Deadline.Add(time.Minute)) {
			return
		}
		time.Sleep(wait)
	}
}

func (s *Server) pumpTerminalBrowser(ctx context.Context, p *terminalPair, input *atomic.Int64, activity *terminal.Activity, refreshDue *atomic.Bool, stop func(string)) {
	// Waiting on a limit stops this read, which is the backpressure: beyond the frames held
	// during the wait, nothing is buffered.
	inputRate := rate.NewLimiter(terminalInputRate, terminal.MaxFrame)
	resizeRate := rate.NewLimiter(terminalResizeRate, terminalResizeBurst)
	early := p.earlyFrames
	lastRefresh := time.Time{}
	for {
		var read terminalRead
		held := false
		if early != nil {
			read, held = <-early
		}
		if !held {
			early = nil
			read.kind, read.frame, read.err = p.browser.Read(context.Background())
		}
		kind, frame, err := read.kind, read.frame, read.err
		if ctx.Err() != nil {
			return // nothing passes once the session has ended
		}
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
			now := activity.Touch()
			if now.Sub(lastRefresh) >= time.Minute {
				lastRefresh = now
				refreshDue.Store(true)
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
		if err = writeTerminal(p.agent, frame); err != nil {
			stop("agent stream lost")
			return
		}
	}
}

func (s *Server) pumpTerminalAgent(ctx context.Context, p *terminalPair, output *atomic.Int64, stop func(string)) {
	outputRate := rate.NewLimiter(terminalOutputRate, terminal.MaxFrame)
	for {
		kind, frame, err := p.agent.Read(context.Background())
		if ctx.Err() != nil {
			return // nothing passes once the session has ended
		}
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
			frame, ending = append([]byte{terminal.Refused}, why...), terminal.RefusedEnding+why
		default:
			stop("invalid frame")
			return
		}
		if err = writeTerminal(p.browser, frame); err != nil {
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
// connections alive, re-reads the conditions a session depends on and keeps the browser
// session alive while the person types. A session whose conditions cannot be read for
// terminalReviewLimit intervals ends: it may no longer be allowed.
func (s *Server) superviseTerminal(ctx context.Context, p *terminalPair, activity *terminal.Activity, refreshDue *atomic.Bool, stop func(string)) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	nextReview, nextPing := time.Now().Add(s.TerminalReviewEvery), time.Now().Add(terminalPing)
	warned, reviewed := time.Time{}, time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			last := activity.Last()
			if time.Since(last) >= s.TerminalIdle {
				stop("idle timeout")
				return
			}
			end := last.Add(s.TerminalIdle)
			if p.stream.Deadline.Before(end) {
				end = p.stream.Deadline
			}
			if time.Until(end) <= time.Minute && warned.Before(end) {
				warned = end
				if err := writeTerminal(p.browser, terminal.WarningFrame(uint16(max(time.Until(end).Seconds(), 0)))); err != nil {
					stop("browser stream lost")
					return
				}
			}
			if now.After(nextReview) {
				nextReview = now.Add(s.TerminalReviewEvery)
				query, cancel := context.WithTimeout(ctx, terminalQuery)
				why, err := s.Execution.ReviewTroubleshootingSession(query, p.stream.Workspace, p.stream.Session)
				cancel()
				if err == nil {
					reviewed = time.Now()
				} else if ctx.Err() == nil {
					s.Log.Error("terminal session review failed", "error", err)
					if time.Since(reviewed) >= terminalReviewLimit*s.TerminalReviewEvery {
						why = "session checks failed"
					}
				}
				if why != "" {
					stop(why)
					return
				}
			}
			if refreshDue.Swap(false) {
				query, cancel := context.WithTimeout(ctx, terminalQuery)
				err := s.Execution.RefreshTroubleshootingBrowser(query, p.stream.Workspace, p.stream.Session, s.Sessions.IdleTimeout, p.stream.Deadline)
				cancel()
				if err != nil && ctx.Err() == nil {
					s.Log.Error("terminal browser refresh failed", "error", err)
				}
			}
			if now.After(nextPing) {
				nextPing = now.Add(terminalPing)
				for _, conn := range []*websocket.Conn{p.browser, p.agent} {
					ping, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// browserPrincipal reads the account a loaded browser session authenticates.
func (s *Server) browserPrincipal(ctx context.Context) (*store.Principal, error) {
	id, err := uuid.Parse(s.Sessions.GetString(ctx, "account"))
	if err != nil {
		return nil, domain.Unauthenticated
	}
	a, err := s.Store.Queries.AccountByID(ctx, id)
	if err != nil {
		return nil, store.IfMissing(err, domain.Unauthenticated)
	}
	if a.AuthVersion != s.Sessions.GetInt64(ctx, "version") {
		return nil, domain.Unauthenticated
	}
	return s.sessionPrincipal(ctx, a), nil
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
