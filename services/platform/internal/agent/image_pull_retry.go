package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// A registry, or the network to it, fails now and then: a timeout, a dropped connection, a busy
// or restarting server. Docker retries a failed layer download itself, so the agent, which reads
// an image before Docker pulls it, reads it again too, a few times within a bounded window. An
// answer that says the image can't be read, such as a refused credential or a missing image, and
// content that differs from its digest, size or the host's limits, are refused at once: reading
// again would get the same answer. Every attempt checks what it reads as the first does, against
// the storage the host has left by then.

// registryRetry is how often, and how long, the agent reads an image again when its registry
// failed for a reason it can recover from.
type registryRetry struct {
	// backoff is the wait before each read after the first, before jitter, and so how many reads
	// the agent makes.
	backoff []time.Duration
	// window is how long after the first read began the agent may still start another.
	window time.Duration
}

var defaultRegistryRetry = registryRetry{backoff: []time.Duration{2 * time.Second, 8 * time.Second}, window: 90 * time.Second}

// wait is how long to wait before the read after attempt, which began the reads elapsed ago,
// given the wait the registry asked for, if any. It is false when the agent reads no more.
func (r registryRetry) wait(attempt int, elapsed, asked time.Duration) (time.Duration, bool) {
	if attempt > len(r.backoff) {
		return 0, false
	}
	base := r.backoff[attempt-1]
	// Between three quarters and five quarters of the backoff, so hosts that failed together
	// don't all ask again at once.
	wait := max(base*3/4+rand.N(base/2+1), asked)
	// asked is the registry's to choose, so it must not overflow when compared.
	if wait > r.window-elapsed {
		return 0, false
	}
	return wait, true
}

// errPullCancelled is the owner cancelling the command while the agent read an image from its
// registry, or waited to read it again.
var errPullCancelled = errors.New("Cancelled while the agent read an image from its registry.")

// readRegistry runs read, one attempt at reading an image from its registry, until it succeeds,
// fails for a reason other than reading the registry, or fails in a way the registry won't
// recover from, within e's retry bounds. It returns errPullCancelled once cancelled, the
// command's owner cancelling it, returns true, ending an attempt it is reading. A read that could not reach the
// registry ends in registryUnreachable's refusal. Each failed read is logged with its cause,
// without credentials.
func (e *Engine) readRegistry(ctx context.Context, image string, cancelled func() bool, read func(context.Context) error) error {
	retry := defaultRegistryRetry
	if e.registryRetry != nil {
		retry = *e.registryRetry
	}
	began := time.Now()
	for attempt := 1; ; attempt++ {
		reading, stop := untilCancelled(ctx, cancelled)
		err := read(reading)
		// Before stop, only the command's own context or its owner ends reading.
		ended := reading.Err() != nil
		stop()
		// Neither is the registry's failure.
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && ended {
			return errPullCancelled
		}
		var unread *imageArchiveUnread
		if !errors.As(err, &unread) {
			return err
		}
		failure := readFailure(unread)
		wait, again := time.Duration(0), false
		cause, transient := registryFailure(failure.err)
		if transient {
			wait, again = retry.wait(attempt, time.Since(began), failure.last.wait)
		}
		log := slog.With("image", truncated(image, 300), "source", failure.source, "attempt", attempt, "cause", cause)
		if !again {
			log.Warn("the agent could not read an image from its registry")
			return registryUnreachable(unread.err)
		}
		log.Warn("the agent could not read an image from its registry and will read it again", "after", wait)
		if !waitUnless(ctx, wait, cancelled) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errPullCancelled
		}
	}
}

// readFailure is the source and failure err, a failure to read an image, names.
func readFailure(err error) *sourceFailure {
	var unread *imageArchiveUnread
	if !errors.As(err, &unread) {
		return &sourceFailure{err: err}
	}
	failure := &sourceFailure{err: unread.err}
	errors.As(unread.err, &failure)
	return failure
}

// waitUnless waits for wait and reports true, or false as soon as ctx ends or cancelled.
func waitUnless(ctx context.Context, wait time.Duration, cancelled func() bool) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if cancelled() {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		case <-timer.C:
			return !cancelled()
		}
	}
}

// transientRegistryFailure reports whether err, a failure reading from a registry, is one the
// registry or the network to it may recover from: a timeout, a refused, reset or dropped
// connection, a temporary name lookup failure, or an answer that says to try again later. Any
// other answer, and any failure of the content itself, is not.
func transientRegistryFailure(err error) bool {
	_, transient := registryFailure(err)
	return transient
}

// registryFailure is what went wrong in err, a failure reading from a registry, and whether the
// registry or the network to it may recover. The cause is in the agent's own words, for its log:
// the error's text can hold what the registry wrote, such as a header that repeats a credential
// or a signed link it redirected to.
func registryFailure(err error) (cause string, transient bool) {
	var response *errcode.ErrorResponse
	if errors.As(err, &response) {
		return "answered " + strconv.Itoa(response.StatusCode), transientStatus(response.StatusCode)
	}
	switch {
	case errors.Is(err, errdef.ErrNotFound):
		return "does not have the content", false
	case errors.Is(err, errLongerThanMeasured):
		return "sent more than the content's size", false
	case errors.Is(err, errShorterThanMeasured):
		return "sent less than the content's size", false
	case errors.Is(err, http.ErrSchemeMismatch):
		return "does not speak HTTPS", false
	case errors.Is(err, errAnswerStalled):
		return "stopped sending its answer", true
	}
	var proxy *proxyRefusal
	if errors.As(err, &proxy) {
		return "the proxy to it answered " + strconv.Itoa(proxy.code), transientStatus(proxy.code)
	}
	var lookup *net.DNSError
	if errors.As(err, &lookup) {
		return "its name could not be looked up", lookup.IsTimeout || lookup.IsTemporary
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "did not answer in time", true
	}
	for _, dropped := range []syscall.Errno{syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.ECONNABORTED, syscall.EPIPE} {
		if errors.Is(err, dropped) {
			return dropped.Error(), true
		}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "closed the connection early", true
	}
	// A registry that resets an HTTP/2 stream drops one answer, as a reset connection does.
	var stream http2.StreamError
	if errors.As(err, &stream) {
		switch stream.Code {
		case http2.ErrCodeNo, http2.ErrCodeInternal, http2.ErrCodeRefusedStream, http2.ErrCodeCancel, http2.ErrCodeEnhanceYourCalm:
			return "reset the answer", true
		}
		return "broke the HTTP/2 protocol", false
	}
	// net/http keeps the type of a connection closed after GOAWAY to itself.
	if errorTextBegins(err, "http2: server sent GOAWAY and closed the connection") {
		return "closed the connection", true
	}
	// A connection the registry closed before it answered.
	var request *url.Error
	if errors.As(err, &request) && errors.Is(request.Err, io.EOF) {
		return "closed the connection before it answered", true
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "its certificate could not be verified", false
	}
	return "gave an answer the agent could not use, or none", false
}

// transientStatus reports whether an answer with this status may differ later.
func transientStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// proxyRefusal is a proxy's answer, other than 200, to a request to connect to a registry.
type proxyRefusal struct{ code int }

func (p *proxyRefusal) Error() string { return "the proxy answered " + strconv.Itoa(p.code) }

// errorTextBegins reports whether err, or an error it wraps, has text that begins with prefix.
func errorTextBegins(err error, prefix string) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if strings.HasPrefix(err.Error(), prefix) {
			return true
		}
	}
	return false
}

// sourceFailure is a failure to read from source, a registry or one of its mirrors, and what the
// source last answered.
type sourceFailure struct {
	source string
	last   registryAnswer
	err    error
}

func (f *sourceFailure) Error() string { return f.err.Error() }
func (f *sourceFailure) Unwrap() error { return f.err }

// registryAnswer is whether a source answered a request, and how long its answer asked to wait.
type registryAnswer struct {
	answered bool
	wait     time.Duration
}

// registryAnswers is what a source has answered.
type registryAnswers struct {
	mu   sync.Mutex
	ever bool
	last registryAnswer
}

// any reports whether the source has answered at all.
func (a *registryAnswers) any() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ever
}

// lastAnswer is what came of the last request to the source.
func (a *registryAnswers) lastAnswer() registryAnswer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// answerTransport records a source's answers, and the wait a busy or unavailable one asks for. It
// ends an answer that its source sends too slowly, see slowAnswer.
type answerTransport struct {
	*http.Transport
	answers  *registryAnswers
	progress registryProgress
}

// registryProgress is how much of an answer a source must send in each window of time the agent
// spends waiting for it. TCP keepalives notice a peer that has gone, but not one that is there and
// sends nothing, or a byte now and then.
type registryProgress struct {
	window time.Duration
	least  int64
}

// defaultRegistryProgress asks for 256 KiB in every 30 seconds spent waiting, about 70 kbit/s.
// An image of 100 MB takes over three hours at that rate, so a link slower still can't pull
// images in practice, and one that keeps up with it passes however much else shares it: only
// time the agent waits counts, so Docker writing slowly never ends an answer, and a window
// that starts fast passes on what it got. A source that stops sending is ended within two
// windows, and an answer that ends within its first window, such as a manifest on a working
// link, is never measured.
var defaultRegistryProgress = registryProgress{window: 30 * time.Second, least: 256 << 10}

func (t *answerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	res, err := t.Transport.RoundTrip(req.WithContext(ctx))
	last := registryAnswer{answered: err == nil}
	if err == nil && (res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusServiceUnavailable) {
		last.wait = retryAfter(res.Header.Get("Retry-After"), time.Now())
	}
	t.answers.mu.Lock()
	t.answers.ever = t.answers.ever || last.answered
	t.answers.last = last
	t.answers.mu.Unlock()
	if err != nil {
		cancel(nil)
		return res, err
	}
	res.Body = newSlowAnswer(res.Body, t.progress, cancel)
	return res, nil
}

// errAnswerStalled is a source that sent less of an answer it had begun than registryProgress
// asks for. It is a timeout, which the agent reads again after.
var errAnswerStalled error = answerStalled{}

type answerStalled struct{}

func (answerStalled) Error() string { return "the registry stopped sending its answer" }
func (answerStalled) Timeout() bool { return true }

// slowAnswer is an answer's body that ends its request, failing with errAnswerStalled, once its
// source has sent less than progress asks for in a window of time spent waiting in reads.
type slowAnswer struct {
	body     io.ReadCloser
	progress registryProgress
	cancel   context.CancelCauseFunc
	// mu guards what follows, which the timer also reads and changes.
	mu sync.Mutex
	// timer ends the window while a read waits.
	timer *time.Timer
	// waited and sent are the window's waiting so far, not counting a read under way, and
	// what the source sent in it.
	waited time.Duration
	sent   int64
	// reading is when the read under way began, zero when none is.
	reading time.Time
	stalled bool
}

func newSlowAnswer(body io.ReadCloser, progress registryProgress, cancel context.CancelCauseFunc) *slowAnswer {
	a := &slowAnswer{body: body, progress: progress, cancel: cancel}
	a.timer = time.AfterFunc(progress.window, a.windowEnds)
	a.timer.Stop()
	return a
}

func (a *slowAnswer) Read(p []byte) (int, error) {
	a.mu.Lock()
	if a.stalled {
		a.mu.Unlock()
		return 0, errAnswerStalled
	}
	a.reading = time.Now()
	a.timer.Reset(a.progress.window - a.waited)
	a.mu.Unlock()
	n, err := a.body.Read(p)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.timer.Stop()
	if !a.reading.IsZero() {
		a.waited += time.Since(a.reading)
		a.reading = time.Time{}
	}
	a.sent += int64(n)
	a.measure()
	if err != nil && err != io.EOF && a.stalled {
		err = errAnswerStalled
	}
	return n, err
}

// windowEnds is the window ending while a read waits.
func (a *slowAnswer) windowEnds() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reading.IsZero() {
		return
	}
	a.waited += time.Since(a.reading)
	a.reading = time.Now()
	a.measure()
	if !a.stalled {
		a.timer.Reset(a.progress.window - a.waited)
	}
}

// measure ends a window that has been waited out: the answer stalls if its source sent too
// little in it, and otherwise the next window begins.
func (a *slowAnswer) measure() {
	if a.waited < a.progress.window {
		return
	}
	if a.sent < a.progress.least {
		a.stalled = true
		a.cancel(errAnswerStalled)
		return
	}
	a.waited, a.sent = 0, 0
}

func (a *slowAnswer) Close() error {
	a.mu.Lock()
	a.timer.Stop()
	a.mu.Unlock()
	err := a.body.Close()
	a.cancel(nil)
	return err
}

// retryAfter is the wait a Retry-After header asks for, in seconds or until a date, or zero.
func retryAfter(header string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseUint(header, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	at, err := http.ParseTime(header)
	if err != nil {
		return 0
	}
	return max(at.Sub(now), 0)
}
