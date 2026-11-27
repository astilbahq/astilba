package outbound

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A refusal under a provider's rate limits is told apart from other refusals, which a later
// request gets again, and says when requests are allowed again.
func TestRateLimit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		until   time.Duration
		limited bool
	}{
		{name: "retry-after", status: 403, headers: map[string]string{"Retry-After": "30"}, until: 30 * time.Second, limited: true},
		{name: "GitHub primary limit reset", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(10 * time.Minute).Unix())}, until: 10 * time.Minute, limited: true},
		{name: "GitLab limit reset", status: 429, headers: map[string]string{"RateLimit-Remaining": "0", "RateLimit-Reset": fmt.Sprint(now.Add(5 * time.Minute).Unix())}, until: 5 * time.Minute, limited: true},
		{name: "secondary limit without headers", status: 403, body: `{"message":"You have exceeded a secondary rate limit."}`, until: time.Minute, limited: true},
		{name: "429 without headers", status: 429, until: time.Minute, limited: true},
		{name: "reset beyond the hour", status: 429, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(48 * time.Hour).Unix())}, until: time.Hour, limited: true},
		{name: "reset already passed", status: 429, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(-time.Minute).Unix())}, until: time.Second, limited: true},
		{name: "forbidden", status: 403, body: `{"message":"Resource not accessible by integration"}`},
		{name: "remaining requests", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "12", "X-RateLimit-Reset": fmt.Sprint(now.Add(time.Minute).Unix())}},
		{name: "GitLab remaining requests", status: 403, headers: map[string]string{"RateLimit-Remaining": "12", "RateLimit-Reset": fmt.Sprint(now.Add(time.Minute).Unix())}},
		{name: "not found", status: 404, headers: map[string]string{"Retry-After": "30"}},
		{name: "unauthorized", status: 401, headers: map[string]string{"Retry-After": "30"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}
			for k, v := range tc.headers {
				res.Header.Set(k, v)
			}
			until, limited := RateLimit(res, now)
			if limited != tc.limited || limited && !until.Equal(now.Add(tc.until)) {
				t.Fatalf("got %v until %v", limited, until.Sub(now))
			}
		})
	}
}
