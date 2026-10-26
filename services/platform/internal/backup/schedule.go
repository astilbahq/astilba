package backup

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func (s Schedule) Validate() error {
	if s.Timezone == "Local" || len(s.Timezone) > 100 {
		return errors.New("invalid schedule timezone")
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return err
	}
	switch s.Kind {
	case "", "daily":
		if s.Hour < 0 || s.Hour > 23 || s.Minute < 0 || s.Minute > 59 || s.Expression != "" || s.IntervalMinutes != 0 {
			return errors.New("invalid daily schedule")
		}
	case "cron":
		if s.Timezone == "" || s.Hour != 0 || s.Minute != 0 || s.IntervalMinutes != 0 || len(s.Expression) > 128 || len(strings.Fields(s.Expression)) != 5 {
			return errors.New("invalid cron schedule")
		}
		parsed, err := scheduleParser.Parse(s.Expression)
		if err != nil {
			return errors.New("invalid cron expression")
		}
		parsed.(*cron.SpecSchedule).Location = loc
		// Five-field cron has no year; validate calendar feasibility against a
		// fixed leap-year cycle rather than the validation process clock.
		if parsed.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, loc)).IsZero() {
			return errors.New("cron has no supported next occurrence")
		}
	case "interval":
		if s.Timezone != "UTC" || s.Hour != 0 || s.Minute != 0 || s.Expression != "" || s.IntervalMinutes < 1 || s.IntervalMinutes > 525600 {
			return errors.New("invalid interval schedule")
		}
	default:
		return errors.New("invalid schedule kind")
	}
	return nil
}

func (p Policy) GeneralSchedule() bool {
	return p.TimeoutSeconds != 0 || p.Schedule.Kind != "" && p.Schedule.Kind != "daily"
}

func (p Policy) RunTimeout() time.Duration {
	if p.TimeoutSeconds == 0 {
		return 30 * time.Minute
	}
	return time.Duration(p.TimeoutSeconds) * time.Second
}

// Next is exclusive. Intervals begin at installation and subsequently advance
// from the persisted cursor, rather than from each execution's completion time.
func (s Schedule) Next(after time.Time) time.Time {
	if s.Kind == "interval" {
		if s.Validate() != nil {
			return time.Time{}
		}
		return after.UTC().Truncate(time.Second).Add(time.Duration(s.IntervalMinutes) * time.Minute)
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}
	}
	expression := s.Expression
	if s.Kind == "" || s.Kind == "daily" {
		expression = fmt.Sprintf("%d %d * * *", s.Minute, s.Hour)
	} else if s.Kind != "cron" || len(expression) > 128 || len(strings.Fields(expression)) != 5 {
		return time.Time{}
	}
	parsed, err := scheduleParser.Parse(expression)
	if err != nil {
		return time.Time{}
	}
	spec := parsed.(*cron.SpecSchedule)
	spec.Location = loc
	for next := spec.Next(after); !next.IsZero(); next = spec.Next(after) {
		local := next.In(loc)
		start, _ := local.ZoneBounds()
		_, offset := local.Zone()
		_, previous := start.Add(-time.Nanosecond).In(loc).Zone()
		if !start.IsZero() && previous > offset {
			end := start.Add(time.Duration(previous-offset) * time.Second)
			if next.Before(end) {
				// The repeated wall minutes already occurred before the fold.
				after = end.Add(-time.Second)
				continue
			}
		}
		return next.UTC()
	}
	return time.Time{}
}

// Due finds the latest missed occurrence without walking every missed minute.
// The persisted interval cursor is its anchor; calendar schedules use monotonic
// Next queries so sparse dates and long outages stay bounded.
func (s Schedule) Due(next, now time.Time) (time.Time, time.Time, error) {
	if next.IsZero() || now.Before(next) || s.Validate() != nil {
		return time.Time{}, time.Time{}, errors.New("invalid due schedule")
	}
	if s.Kind == "interval" {
		d := time.Duration(s.IntervalMinutes) * time.Minute
		at := next.Add(now.Sub(next) / d * d)
		return at, at.Add(d), nil
	}
	low, high := next.Unix()/60-1, now.Unix()/60
	for low < high {
		mid := low + (high-low+1)/2
		candidate := s.Next(time.Unix(mid*60, 0))
		if !candidate.IsZero() && !candidate.After(now) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	at, future := s.Next(time.Unix(low*60, 0)), s.Next(now)
	if at.IsZero() || at.Before(next) || at.After(now) || future.IsZero() || !future.After(now) {
		return time.Time{}, time.Time{}, errors.New("no supported schedule occurrence")
	}
	return at, future, nil
}
