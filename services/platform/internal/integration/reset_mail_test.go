package integration

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const resetMailKind = "platform_reset_mail_v1"

// resetRelay is a relay a test can make refuse mail, or stop at one of the moments a send can be
// in flight: holdBefore before the message, holdAfter with the message read but not answered.
// With drop it drops the connection at that second moment instead of answering.
type resetRelay struct {
	messages   chan string
	refuse     atomic.Bool
	holdBefore atomic.Bool
	holdAfter  atomic.Bool
	drop       atomic.Bool
	reached    chan struct{}
	release    chan struct{}
	released   sync.Once
}

func (r *resetRelay) letGo() { r.released.Do(func() { close(r.release) }) }

func (r *resetRelay) hold() {
	r.reached <- struct{}{}
	<-r.release
}

func newResetRelay(t *testing.T, f *fixture) *resetRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	relay := &resetRelay{messages: make(chan string, 10), reached: make(chan struct{}, 10), release: make(chan struct{})}
	t.Cleanup(relay.letGo)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go relay.serve(conn)
		}
	}()
	sender, err := mailer.New(mailer.Config{Address: listener.Addr().String(), From: "Astilba <accounts@example.test>", Mode: "plain", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	f.auth.Mail = sender
	return relay
}

func (r *resetRelay) serve(conn net.Conn) {
	defer conn.Close()
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	reply := func(s string) { _, _ = rw.WriteString(s + "\r\n"); _ = rw.Flush() }
	reply("220 test SMTP")
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "MAIL") && r.refuse.Load():
			reply("451 try later")
		case strings.HasPrefix(line, "MAIL") && r.holdBefore.Load():
			r.hold()
			reply("250 OK")
		case strings.HasPrefix(line, "DATA"):
			reply("354 send message")
			body, err := textproto.NewReader(rw.Reader).ReadDotBytes()
			if err != nil {
				return
			}
			r.messages <- string(body)
			if r.drop.Load() {
				return
			}
			if r.holdAfter.Load() {
				r.hold()
			}
			reply("250 queued")
		case strings.HasPrefix(line, "QUIT"):
			reply("221 bye")
			return
		default:
			reply("250 OK")
		}
	}
}

func (r *resetRelay) token(t *testing.T, f *fixture) string {
	t.Helper()
	fixtureMail := f.mail
	f.mail = r.messages
	defer func() { f.mail = fixtureMail }()
	return tokenFromLinkMail(t, f, "/reset")
}

// receive fails the one test, rather than the whole package at its timeout, if nothing arrives.
func receive[T any](t *testing.T, from <-chan T) T {
	t.Helper()
	select {
	case value := <-from:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("the relay was not reached")
		panic("unreachable")
	}
}

func requestReset(t *testing.T, f *fixture, email string) {
	t.Helper()
	f.browser(t).call("POST", "/api/v1/auth/reset/request", map[string]string{"email": email}, 202, nil)
}

// waitForResetJob waits until the newest reset job is in one of the states.
func waitForResetJob(t *testing.T, f *fixture, states ...string) {
	t.Helper()
	var state string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT state FROM river_job WHERE kind=$1 ORDER BY id DESC LIMIT 1", resetMailKind).Scan(&state); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if slices.Contains(states, state) {
			return
		}
	}
	t.Fatalf("the reset job stayed %q, want %v", state, states)
}

func waitForResetJobsToFinish(t *testing.T, f *fixture) {
	t.Helper()
	var unfinished int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind=$1 AND state<>'completed'", resetMailKind).Scan(&unfinished); err != nil {
			t.Fatal(err)
		}
		if unfinished == 0 {
			return
		}
	}
	t.Fatalf("%d reset jobs did not finish", unfinished)
}

// resetToken reads a link from a message without waiting for it to work.
func resetToken(t *testing.T, f *fixture, message string) string {
	t.Helper()
	fields := strings.Fields(message)
	token, found := strings.CutPrefix(fields[len(fields)-1], f.apiServer.Origin+"/reset?token=")
	if !found {
		t.Fatalf("not a reset link: %q", fields[len(fields)-1])
	}
	return token
}

// retryResetJobs runs waiting jobs now rather than after their backoff.
func retryResetJobs(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), "UPDATE river_job SET state='available',scheduled_at=now() WHERE kind=$1 AND state='retryable'", resetMailKind); err != nil {
		t.Fatal(err)
	}
}

func wantResetLink(t *testing.T, f *fixture, token string, want bool, what string) {
	t.Helper()
	_, err := f.st.Queries.PeekPasswordReset(context.Background(), auth.Hash(token))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if (err == nil) != want {
		t.Fatalf("%s: link working=%v, want %v", what, err == nil, want)
	}
}

type statementTrace struct {
	mu         sync.Mutex
	statements []string
}

func (s *statementTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statements = append(s.statements, data.SQL)
	return ctx
}

func (*statementTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (s *statementTrace) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	taken := s.statements
	s.statements = nil
	return taken
}

// A request must not be able to behave differently for an email with an account, so this compares
// what it does rather than how long it takes: the same statements, none reading accounts or links,
// with no mailer to call.
func TestResetRequestDoesTheSameWorkForAnyEmail(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(f.st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	trace := &statementTrace{}
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	queue, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	requests := &auth.Service{Store: store.New(pool, f.st.Broker, false), Now: time.Now, Queue: queue, Tokens: f.auth.Tokens}
	work := func(email string) []string {
		t.Helper()
		trace.take()
		if err := requests.RequestReset(ctx, email, auth.Source{Key: "198.51.100.1", Network: "198.51.100.1"}); err != nil {
			t.Fatal(err)
		}
		// River wakes its workers at most once per interval, whoever the job is for.
		return slices.DeleteFunc(trace.take(), func(statement string) bool { return strings.Contains(statement, "pg_notify") })
	}
	work("warm-up@example.test")
	known, unknown := work("owner@example.test"), work("nobody@example.test")
	if len(known) == 0 || !slices.Equal(known, unknown) {
		t.Fatalf("known email ran %q, unknown email ran %q", known, unknown)
	}
	for _, statement := range known {
		if strings.Contains(statement, "accounts") || strings.Contains(statement, "password_resets") {
			t.Fatalf("a reset request read account state: %s", statement)
		}
	}
	var args string
	if err := f.st.Pool.QueryRow(ctx, "SELECT string_agg(args::text,'') FROM river_job WHERE kind=$1", resetMailKind).Scan(&args); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(args, "example.test\"") || strings.Contains(args, "owner@") || strings.Contains(args, "nobody@") {
		t.Fatal("a queued request holds the email in clear")
	}
}

func TestUnknownEmailLeavesNothingForAnAccountCreatedLater(t *testing.T) {
	f := setup(t)
	const email = "newcomer@example.test"
	for range 4 {
		requestReset(t, f, email)
	}
	waitForResetJobsToFinish(t, f)
	select {
	case <-f.mail:
		t.Fatal("an unknown email was sent mail")
	default:
	}
	if got := attemptCount(t, f, "reset:email:"+email); got != 0 {
		t.Fatalf("an unknown email was charged %d attempts", got)
	}
	if _, err := f.st.Queries.InsertAccount(context.Background(), db.InsertAccountParams{ID: uuid.New(), Email: email, Name: "Newcomer", PasswordHash: "unused"}); err != nil {
		t.Fatal(err)
	}
	requestReset(t, f, email)
	if tokenFromLinkMail(t, f, "/reset") == "" {
		t.Fatal("no link was sent")
	}
}

func TestFailedSendKeepsTheEarlierLinkAndRetries(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	const email = "owner@example.test"
	requestReset(t, f, email)
	earlier := relay.token(t, f)
	relay.refuse.Store(true)
	requestReset(t, f, email)
	waitForResetJob(t, f, "retryable")
	wantResetLink(t, f, earlier, true, "after a failed send")
	if got := attemptCount(t, f, "reset:email:"+email); got != 1 {
		t.Fatalf("a failed send left %d attempts, want 1", got)
	}
	if logged := f.log.String(); !strings.Contains(logged, "password reset mail could not be sent") || strings.Contains(logged, email) {
		t.Fatalf("the failed send was not logged safely: %q", logged)
	}
	relay.refuse.Store(false)
	retryResetJobs(t, f)
	newest := relay.token(t, f)
	wantResetLink(t, f, earlier, false, "after the retried send")
	wantResetLink(t, f, newest, true, "after the retried send")
	if got := attemptCount(t, f, "reset:email:"+email); got != 2 {
		t.Fatalf("two sent links left %d attempts, want 2", got)
	}
}

func TestEmailBudgetHoldsAgainstConcurrentJobs(t *testing.T) {
	f := setup(t)
	const email = "owner@example.test"
	relay := newResetRelay(t, f)
	relay.holdAfter.Store(true)
	for range 8 {
		requestReset(t, f, email)
	}
	for range 3 {
		receive(t, relay.reached)
	}
	relay.letGo()
	waitForResetJobsToFinish(t, f)
	if len(relay.messages) != 3 {
		t.Fatalf("%d mails sent, want 3", len(relay.messages))
	}
}

func TestCredentialChangeDuringASendEndsItsLink(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	relay.holdAfter.Store(true)
	requestReset(t, f, "owner@example.test")
	receive(t, relay.reached)
	if err := f.st.Queries.UpdatePassword(context.Background(), db.UpdatePasswordParams{ID: f.owner, PasswordHash: "replaced"}); err != nil {
		t.Fatal(err)
	}
	relay.letGo()
	token := resetToken(t, f, receive(t, relay.messages))
	waitForResetJob(t, f, "completed")
	wantResetLink(t, f, token, false, "after the password changed during its send")
}

// Stopping a server cancels running jobs. A send cut off before its message must run again, with
// its charge undone and the earlier link intact. A request made while nothing runs must wait.
func TestResetMailSurvivesAServerStopping(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	const email = "owner@example.test"
	stopWorkers := func() {
		t.Helper()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.execution.Queue.StopAndCancel(stop); err != nil {
			t.Fatal("the send held up stopping", err)
		}
	}
	requestReset(t, f, email)
	earlier := relay.token(t, f)
	relay.holdBefore.Store(true)
	requestReset(t, f, email)
	receive(t, relay.reached)
	stopWorkers()
	waitForResetJob(t, f, "retryable")
	wantResetLink(t, f, earlier, true, "after the send was cut off")
	if got := attemptCount(t, f, "reset:email:"+email); got != 1 {
		t.Fatalf("the send that was cut off left %d attempts, want 1", got)
	}
	relay.holdBefore.Store(false)
	retryResetJobs(t, f)
	if err := f.execution.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	retried := relay.token(t, f)
	wantResetLink(t, f, earlier, false, "after the send ran again")
	waitForResetJobsToFinish(t, f)

	stopWorkers()
	requestReset(t, f, email)
	if len(relay.messages) != 0 {
		t.Fatal("mail was sent with no server running")
	}
	if err := f.execution.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if newest := relay.token(t, f); newest == retried {
		t.Fatal("two requests sent the same link")
	}
}

// A message the relay read but never answered may arrive, so however many times that repeats,
// no more than the budget's worth may be attempted.
func TestUnansweredSendsStayWithinTheEmailBudget(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	relay.drop.Store(true)
	const email = "owner@example.test"
	requestReset(t, f, email)
	for attempt := int32(1); attempt <= 3; attempt++ {
		waitForResetJob(t, f, "retryable")
		if got := attemptCount(t, f, "reset:email:"+email); got != attempt {
			t.Fatalf("after %d unanswered sends the budget held %d attempts", attempt, got)
		}
		retryResetJobs(t, f)
	}
	waitForResetJobsToFinish(t, f)
	if len(relay.messages) != 3 {
		t.Fatalf("the relay read %d messages, want 3", len(relay.messages))
	}
}

func TestRetryDoesNotReplaceANewerLink(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	relay.refuse.Store(true)
	const email = "owner@example.test"
	requestReset(t, f, email)
	waitForResetJob(t, f, "retryable")
	relay.refuse.Store(false)
	requestReset(t, f, email)
	newer := relay.token(t, f)
	retryResetJobs(t, f)
	waitForResetJobsToFinish(t, f)
	wantResetLink(t, f, newer, true, "after an older request's retry")
	if len(relay.messages) != 0 {
		t.Fatal("the older request's retry sent mail")
	}
}

// Sends now end when their caller does, so what was written for them has to be undone without it.
func TestHangingUpDuringAnInvitationSendLeavesNoInvitation(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	owner := f.browser(t)
	workspace := owner.login("owner@example.test", f.password).Workspaces[0].Id
	relay.holdBefore.Store(true)
	ctx, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	request, err := newRequest(owner, "POST", "/api/v1/workspaces/"+workspace.String()+"/invitations", []byte(`{"email":"guest@example.test","role":"viewer"}`))
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		response, err := owner.client.Do(request.WithContext(ctx))
		if err == nil {
			response.Body.Close()
		}
		finished <- err
	}()
	receive(t, relay.reached)
	hangUp()
	receive(t, finished)
	var invitations int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM invitations").Scan(&invitations); err != nil {
			t.Fatal(err)
		}
		if invitations == 0 {
			return
		}
	}
	t.Fatal("an invitation nobody was sent is still pending")
}

func TestStoppingDuringAHostedInvitationSendLeavesNoInvitation(t *testing.T) {
	f := setupBootstrap(t, false)
	f.st.Hosted = true
	f.st.Hosting = hosting.Config{CohortLimit: 1, ServerLimit: 2}
	relay := newResetRelay(t, f)
	relay.holdBefore.Store(true)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	result := make(chan error, 1)
	go func() { result <- f.auth.InviteHosted(ctx, "beta@example.test", f.apiServer.Origin) }()
	receive(t, relay.reached)
	stop()
	if err := receive(t, result); !errors.Is(err, mailer.ErrUnavailable) {
		t.Fatal(err)
	}
	var invitations int
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM hosted_invitations").Scan(&invitations); err != nil {
		t.Fatal(err)
	}
	if invitations != 0 {
		t.Fatal("an invitation nobody was sent still takes a place in the cohort")
	}
}

func TestResetMailIsAbandonedVisiblyAfterItsPatience(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	relay.refuse.Store(true)
	const email = "owner@example.test"
	requestReset(t, f, email)
	waitForResetJob(t, f, "retryable")
	if _, err := f.st.Pool.Exec(context.Background(), "UPDATE river_job SET created_at=now()-interval '16 minutes' WHERE kind=$1", resetMailKind); err != nil {
		t.Fatal(err)
	}
	relay.refuse.Store(false)
	retryResetJobs(t, f)
	waitForResetJob(t, f, "cancelled")
	if len(relay.messages) != 0 {
		t.Fatal("mail was sent after the person who asked had been told to ask again")
	}
	if logged := f.log.String(); !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "password reset mail abandoned") || strings.Contains(logged, email) {
		t.Fatalf("giving up was not logged safely: %q", logged)
	}
}

func TestFailedRefundIsLogged(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	relay.refuse.Store(true)
	if _, err := f.st.Pool.Exec(context.Background(), `CREATE FUNCTION refuse_refund() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse_refund BEFORE UPDATE ON auth_attempts FOR EACH ROW WHEN (NEW.attempts < OLD.attempts) EXECUTE FUNCTION refuse_refund()`); err != nil {
		t.Fatal(err)
	}
	const email = "owner@example.test"
	requestReset(t, f, email)
	waitForResetJob(t, f, "retryable")
	if got := attemptCount(t, f, "reset:email:"+email); got != 1 {
		t.Fatalf("the refused refund left %d attempts, want 1", got)
	}
	if logged := f.log.String(); !strings.Contains(logged, "password reset charge could not be refunded") || strings.Contains(logged, email) {
		t.Fatalf("the failed refund was not logged safely: %q", logged)
	}
}

func TestResetRequestIsKeptAfterTheRequesterHangsUp(t *testing.T) {
	f := setup(t)
	ctx, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	held, err := f.st.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(context.Background())
	// Only this insert waits, so River's own queries cannot use up a small pool meanwhile.
	if _, err := f.st.Pool.Exec(context.Background(), `CREATE FUNCTION hold_reset_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(4281642399); RETURN NEW; END $$;
		CREATE TRIGGER hold_reset_job BEFORE INSERT ON river_job FOR EACH ROW WHEN (NEW.kind='`+resetMailKind+`') EXECUTE FUNCTION hold_reset_job()`); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(ctx, "SELECT pg_advisory_xact_lock(4281642399)"); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- f.auth.RequestReset(ctx, "owner@example.test", auth.Source{Key: "test", Network: "test"})
	}()
	for deadline := time.Now().Add(10 * time.Second); attemptCount(t, f, "reset:ip:test") == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the reset request did not reach the queue")
		}
	}
	hangUp()
	if err := held.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal("the request ended with the client", err)
	}
	if tokenFromLinkMail(t, f, "/reset") == "" {
		t.Fatal("no link was sent")
	}
}
