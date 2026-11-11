package store

import (
	"testing"
	"time"
)

// A proof of credentials counts for five minutes. A recorded time slightly ahead of the
// reading clock, from a small backwards step or a second process slightly ahead, is a
// proof made just now. Beyond the allowance it fails closed, and the allowance never
// makes a proof count for longer than five minutes from the reading.
func TestRecentAuthenticationAllowsOnlyASmallSkew(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		at    time.Time
		until time.Time // zero when the proof does not count
	}{
		{"never proved", time.Time{}, time.Time{}},
		{"just now", now, now.Add(5 * time.Minute)},
		{"four minutes ago", now.Add(-4 * time.Minute), now.Add(time.Minute)},
		{"exactly five minutes ago", now.Add(-5 * time.Minute), now},
		{"stale", now.Add(-5*time.Minute - time.Nanosecond), time.Time{}},
		{"long ago", now.Add(-time.Hour), time.Time{}},
		{"a second ahead", now.Add(time.Second), now.Add(5 * time.Minute)},
		{"at the allowance", now.Add(recentAuthenticationSkew), now.Add(5 * time.Minute)},
		{"past the allowance", now.Add(recentAuthenticationSkew + time.Nanosecond), time.Time{}},
		{"far ahead", now.Add(time.Hour), time.Time{}},
	} {
		if got := Recent(tc.at, now); got != !tc.until.IsZero() {
			t.Errorf("%s: recent %v", tc.name, got)
		}
		if got := RecentUntil(tc.at, now); !got.Equal(tc.until) {
			t.Errorf("%s: recent until %s, want %s", tc.name, got, tc.until)
		}
	}
	if recentAuthenticationSkew > 10*time.Second {
		t.Fatalf("an allowance of %s is not small", recentAuthenticationSkew)
	}
}
