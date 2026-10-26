package backup

import (
	"encoding/json"
	"testing"
	"time"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestCronCalendarAndFirstWallOccurrence(t *testing.T) {
	for _, c := range []struct{ expression, zone, after, want string }{
		{"*/15 9-10 * JAN,MAR MON-FRI", "UTC", "2026-01-05T09:00:00Z", "2026-01-05T09:15:00Z"},
		{"0 9 15 * MON", "UTC", "2026-01-06T00:00:00Z", "2026-01-12T09:00:00Z"},
		{"0 0 29 FEB *", "UTC", "2026-02-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"30 1 * * *", "Europe/London", "2026-03-28T02:00:00Z", "2026-03-30T00:30:00Z"},
		{"30 1 * * *", "Europe/London", "2026-10-25T00:31:00Z", "2026-10-26T01:30:00Z"},
		{"* * * * *", "Europe/London", "2026-10-25T00:59:00Z", "2026-10-25T02:00:00Z"},
		{"45 1 * * *", "Australia/Lord_Howe", "2026-04-04T15:01:00Z", "2026-04-05T15:15:00Z"},
	} {
		s := Schedule{Kind: "cron", Expression: c.expression, Timezone: c.zone}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		if got := s.Next(instant(t, c.after)); !got.Equal(instant(t, c.want)) {
			t.Errorf("%s %s after%s: got%s want%s", c.expression, c.zone, c.after, got, c.want)
		}
	}
}

func TestScheduleRejectsAmbiguousAndUnboundedInputs(t *testing.T) {
	for _, s := range []Schedule{
		{Kind: "unknown", Timezone: "UTC"},
		{Kind: "cron", Expression: "* * * * *", Timezone: ""},
		{Kind: "cron", Expression: "CRON_TZ=UTC * * * * *", Timezone: "UTC"},
		{Kind: "cron", Expression: "* * * * * *", Timezone: "UTC"},
		{Kind: "cron", Expression: "0 0 31 FEB *", Timezone: "UTC"},
		{Kind: "cron", Expression: "* * * * *", Timezone: "Local"},
		{Kind: "cron", Expression: "* * * * *", Timezone: "UTC", Hour: 1},
		{Kind: "daily", Timezone: "UTC", Expression: "* * * * *"},
		{Kind: "interval", Timezone: "UTC", IntervalMinutes: 0},
		{Kind: "interval", Timezone: "UTC", IntervalMinutes: 525601},
		{Kind: "interval", Timezone: "Europe/London", IntervalMinutes: 60},
	} {
		if s.Validate() == nil {
			t.Errorf("accepted%+v", s)
		}
	}
}

func TestDueCoalescesLongOutagesWithoutMovingIntervalAnchor(t *testing.T) {
	for _, c := range []struct {
		s                     Schedule
		next, now, at, future string
	}{
		{Schedule{Kind: "cron", Expression: "*/15 * * * *", Timezone: "UTC"}, "2000-01-01T00:00:00Z", "2026-10-26T12:34:59Z", "2026-10-26T12:30:00Z", "2026-10-26T12:45:00Z"},
		{Schedule{Kind: "interval", IntervalMinutes: 7, Timezone: "UTC"}, "2026-10-26T10:00:23Z", "2026-10-26T12:01:00Z", "2026-10-26T11:59:23Z", "2026-10-26T12:06:23Z"},
		{Schedule{Kind: "cron", Expression: "0 0 29 FEB *", Timezone: "UTC"}, "2000-02-29T00:00:00Z", "2026-10-26T12:34:59Z", "2024-02-29T00:00:00Z", "2028-02-29T00:00:00Z"},
	} {
		at, future, err := c.s.Due(instant(t, c.next), instant(t, c.now))
		if err != nil || !at.Equal(instant(t, c.at)) || !future.Equal(instant(t, c.future)) {
			t.Fatalf("due%+v: at%s next%s err%v", c.s, at, future, err)
		}
	}
}

func TestLegacyBlankTimezonePreservesWireAndUTCBehavior(t *testing.T) {
	const wire = `{"hour":2,"minute":30,"timezone":""}`
	var s Schedule
	if err := json.Unmarshal([]byte(wire), &s); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	if string(raw) != wire {
		t.Fatalf("legacy digest input changed: %s", raw)
	}
	if got := s.Next(instant(t, "2026-10-26T02:31:00Z")); !got.Equal(instant(t, "2026-10-27T02:30:00Z")) {
		t.Fatal(got)
	}
}
