package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http2"
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
	// attempts is how many reads the agent makes in all.
	attempts int
	// backoff is the wait before each read after the first, before jitter.
	backoff []time.Duration
	// window is how long after the first read began the agent may still start another.
	window time.Duration
}

var defaultRegistryRetry = registryRetry{attempts: 3, backoff: []time.Duration{2 * time.Second, 8 * time.Second}, window: 90 * time.Second}

// wait is how long to wait before the read after attempt, which began the reads elapsed ago,
// given the wait the registry asked for, if any. It is false when the agent reads no more.
func (r registryRetry) wait(attempt int, elapsed, asked time.Duration) (time.Duration, bool) {
	if attempt >= r.attempts || attempt > len(r.backoff) {
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

// readRegistry runs read, one attempt at reading an image from its registry, until it succeeds,
// fails for a reason other than reading the registry, or fails in a way the registry won't
// recover from, within e's retry bounds. It starts no read after the first once cancelled, the
// command's owner cancelling it, returns true. A read that could not reach the registry ends in
// registryUnreachable's refusal. Each failed read is logged with its cause, without credentials.
func (e *Engine) readRegistry(ctx context.Context, image string, cancelled func() bool, read func(context.Context) error) error {
	retry := defaultRegistryRetry
	if e.registryRetry != nil {
		retry = *e.registryRetry
	}
	began := time.Now()
	for attempt := 1; ; attempt++ {
		answers := &registryAnswers{}
		err := read(context.WithValue(ctx, registryAnswersKey{}, answers))
		// The end of the command's own context is not the registry's failure.
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		var unread *imageArchiveUnread
		if !errors.As(err, &unread) {
			return err
		}
		wait, again := time.Duration(0), false
		if transientRegistryFailure(unread.err) {
			wait, again = retry.wait(attempt, time.Since(began), answers.asked())
		}
		slog.Warn("the agent could not read an image from its registry", "image", truncated(image, 300), "attempt", attempt, "error", registryFailureText(unread.err), "readsAgain", again)
		if !again || !waitUnless(ctx, wait, cancelled) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return registryUnreachable(unread.err)
		}
	}
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
	var response *errcode.ErrorResponse
	if errors.As(err, &response) {
		return response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	}
	var lookup *net.DNSError
	if errors.As(err, &lookup) {
		return lookup.IsTimeout || lookup.IsTemporary
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	for _, dropped := range []error{syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.ECONNABORTED, syscall.EPIPE, io.ErrUnexpectedEOF} {
		if errors.Is(err, dropped) {
			return true
		}
	}
	// A registry that resets an HTTP/2 stream drops one answer, as a reset connection does.
	var stream http2.StreamError
	if errors.As(err, &stream) {
		switch stream.Code {
		case http2.ErrCodeNo, http2.ErrCodeInternal, http2.ErrCodeRefusedStream, http2.ErrCodeCancel, http2.ErrCodeEnhanceYourCalm:
			return true
		}
		return false
	}
	// A connection the registry closed before it answered.
	var request *url.Error
	return errors.As(err, &request) && errors.Is(request.Err, io.EOF)
}

// registryFailureText describes err, a failure reading from a registry, for the agent's log. A
// registry may answer with any text, and may redirect a blob to storage behind a signed,
// short-lived link, so an answer is only its status and host, and a URL only its scheme and host.
func registryFailureText(err error) string {
	var response *errcode.ErrorResponse
	if errors.As(err, &response) {
		text := "the registry answered " + strconv.Itoa(response.StatusCode)
		if response.URL != nil {
			text = response.URL.Host + " answered " + strconv.Itoa(response.StatusCode)
		}
		return truncated(text, 300)
	}
	text := err.Error()
	var request *url.Error
	if errors.As(err, &request) {
		text = request.Op + ": " + request.Err.Error()
		if u, err := url.Parse(request.URL); err == nil && u.Host != "" {
			text = request.Op + " " + u.Host + ": " + request.Err.Error()
		}
	}
	text = urlInText.ReplaceAllStringFunc(text, func(text string) string {
		if u, err := url.Parse(text); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
		return "a URL"
	})
	return truncated(queryInText.ReplaceAllString(text, ""), 300)
}

var (
	urlInText   = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]*`)
	queryInText = regexp.MustCompile(`\?[^\s"'<>]*`)
)

// registryAnswers records the longest wait a registry asked for, with Retry-After, in its
// answers to one attempt's reads. A registry that asks for more than the agent's window allows is
// not asked again.
type registryAnswers struct {
	mu   sync.Mutex
	wait time.Duration
}

type registryAnswersKey struct{}

func (a *registryAnswers) asked() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wait
}

func (a *registryAnswers) note(wait time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.wait = max(a.wait, wait)
}

// retryAfterTransport notes the wait a registry asks for in a busy or unavailable answer, for the
// attempt whose request it answered.
type retryAfterTransport struct{ base http.RoundTripper }

func (t retryAfterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil || res.StatusCode != http.StatusTooManyRequests && res.StatusCode != http.StatusServiceUnavailable {
		return res, err
	}
	if answers, ok := req.Context().Value(registryAnswersKey{}).(*registryAnswers); ok {
		if wait, ok := retryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
			answers.note(wait)
		}
	}
	return res, err
}

// retryAfter is the wait a Retry-After header asks for, in seconds or until a date.
func retryAfter(header string, now time.Time) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(header, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	at, err := http.ParseTime(header)
	if err != nil {
		return 0, false
	}
	return max(at.Sub(now), 0), true
}
