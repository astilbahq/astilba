package outbound

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimit reads whether res refuses a request under the server's rate limits, and when it
// allows requests again: retry-after when present, then a used-up limit's reset, and otherwise a
// minute. GitHub names its limit's headers X-RateLimit-*, and GitLab RateLimit-*. GitHub refuses
// over a limit with 403 as well as 429, and a 403 without those signals is a rate limit only if
// its body says so, so the body is read, up to 4 KiB, from a 403 that has none of them. A refusal
// for any other reason, which a later request gets again, is not one.
func RateLimit(res *http.Response, now time.Time) (time.Time, bool) {
	if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	until := now.Add(time.Minute)
	if seconds, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && seconds > 0 {
		until = now.Add(time.Duration(seconds) * time.Second)
	} else if reset, spent := limitReset(res.Header); spent {
		until = reset
	} else if res.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if !strings.Contains(strings.ToLower(string(body)), "rate limit") {
			return time.Time{}, false
		}
	}
	// Providers' limits reset within the hour, so a later time is a header to distrust.
	if until.Before(now.Add(time.Second)) {
		until = now.Add(time.Second)
	}
	if until.After(now.Add(time.Hour)) {
		until = now.Add(time.Hour)
	}
	return until, true
}

// limitReset reads when a used-up rate limit resets, from either provider's headers.
func limitReset(h http.Header) (time.Time, bool) {
	for _, prefix := range []string{"X-RateLimit-", "RateLimit-"} {
		reset, e := strconv.ParseInt(h.Get(prefix+"Reset"), 10, 64)
		if e == nil && h.Get(prefix+"Remaining") == "0" {
			return time.Unix(reset, 0), true
		}
	}
	return time.Time{}, false
}
