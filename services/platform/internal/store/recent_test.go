package store

import (
	"testing"
	"time"
)

// A proof of credentials counts for fifteen minutes, or five for changes to credentials and
// access. A recorded time slightly ahead of the reading clock, from a small backwards step or
// a second process slightly ahead, is a proof made just now. Beyond the allowance it fails
// closed, and the allowance never makes a proof count for longer than its window from the
// reading.
func TestRecentAuthenticationAllowsOnlyASmallSkew(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, window := range []struct {
		name   string
		length time.Duration
		until  func(at, now time.Time) time.Time
	}{
		{"recent", 15 * time.Minute, RecentUntil},
		{"credential", 5 * time.Minute, CredentialRecentUntil},
	} {
		w := window.length
		for _, tc := range []struct {
			name  string
			at    time.Time
			until time.Time // zero when the proof does not count
		}{
			{"never proved", time.Time{}, time.Time{}},
			{"just now", now, now.Add(w)},
			{"a minute before the end", now.Add(time.Minute - w), now.Add(time.Minute)},
			{"exactly the window ago", now.Add(-w), now},
			{"stale", now.Add(-w - time.Nanosecond), time.Time{}},
			{"long ago", now.Add(-time.Hour), time.Time{}},
			{"a second ahead", now.Add(time.Second), now.Add(w)},
			{"at the allowance", now.Add(recentAuthenticationSkew), now.Add(w)},
			{"past the allowance", now.Add(recentAuthenticationSkew + time.Nanosecond), time.Time{}},
			{"far ahead", now.Add(time.Hour), time.Time{}},
		} {
			if got := window.until(tc.at, now); !got.Equal(tc.until) {
				t.Errorf("%s, %s: until %s, want %s", window.name, tc.name, got, tc.until)
			}
		}
	}
	if !Recent(now.Add(-15*time.Minute), now) || Recent(now.Add(-15*time.Minute-time.Nanosecond), now) {
		t.Error("Recent does not match its fifteen-minute window")
	}
	// A sign-in waiting for its second factor keeps the shorter window.
	if !PendingSignIn(now.Add(-5*time.Minute), now) || PendingSignIn(now.Add(-5*time.Minute-time.Nanosecond), now) {
		t.Error("a pending sign-in does not wait exactly five minutes")
	}
	if recentAuthenticationSkew > 10*time.Second {
		t.Fatalf("an allowance of %s is not small", recentAuthenticationSkew)
	}
}
