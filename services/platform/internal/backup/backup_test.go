package backup

import (
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestDailyScheduleDSTAndTimezone(t *testing.T) {
	cases := []struct {
		zone, after, want string
		hour, minute      int
	}{
		{"Europe/London", "2026-03-28T02:00:00Z", "2026-03-30T00:30:00Z", 1, 30},
		{"Europe/London", "2026-10-25T00:31:00Z", "2026-10-26T01:30:00Z", 1, 30},
		{"Europe/London", "2026-10-24T12:00:00Z", "2026-10-25T00:30:00Z", 1, 30},
		{"Australia/Lord_Howe", "2026-04-04T15:01:00Z", "2026-04-05T15:15:00Z", 1, 45},
		{"America/New_York", "2026-03-08T06:00:00Z", "2026-03-09T06:30:00Z", 2, 30},
	}
	for _, c := range cases {
		after, _ := time.Parse(time.RFC3339, c.after)
		want, _ := time.Parse(time.RFC3339, c.want)
		got := (Schedule{Timezone: c.zone, Hour: c.hour, Minute: c.minute}).Next(after)
		if !got.Equal(want) {
			t.Errorf("%s after %s: got %s want %s", c.zone, after, got, want)
		}
	}
}
func TestRetentionProtectsRecoveryPointAndRestoreAcrossDuplicateRecords(t *testing.T) {
	now := time.Now()
	runs := []Run{}
	for i := range 5 {
		r := Run{ID: uuid.New(), State: "succeeded", Snapshot: strings.Repeat(string(rune('a'+i)), 64), ScheduledAt: now.Add(time.Duration(i) * time.Hour)}
		runs = append(runs, r)
	}
	runs[0].Verified = true
	runs[1].Protected = true
	duplicate := runs[1]
	duplicate.ID = uuid.New()
	duplicate.Protected = false
	runs = append(runs, duplicate)
	failed := Run{State: "failed", Snapshot: strings.Repeat("f", 64)}
	runs = append(runs, failed)
	got := Prunable(runs, 1)
	if len(got) != 2 || got[0] != runs[3].Snapshot || got[1] != runs[2].Snapshot {
		t.Fatalf("unexpected retention candidates: %v", got)
	}
}
