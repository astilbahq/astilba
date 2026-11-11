package terminal

import (
	"sync/atomic"
	"time"
)

// Activity records when a person last typed, for the idle limit. It keeps an offset from
// a reading of the monotonic clock, so stepping the wall clock, by NTP or by hand, cannot
// shorten or lengthen the limit. Unix nanoseconds would drop that reading. It is safe for
// concurrent use, and its zero value is not: use NewActivity.
type Activity struct {
	origin time.Time
	since  atomic.Int64 // nanoseconds from origin to the last input
}

// NewActivity starts counting idle time from now.
func NewActivity() *Activity { return &Activity{origin: time.Now()} }

// Touch records input now and returns that time.
func (a *Activity) Touch() time.Time {
	now := time.Now()
	a.since.Store(int64(now.Sub(a.origin)))
	return now
}

// Last is when input last arrived. It keeps the monotonic reading, so subtracting it from
// another time.Now measures elapsed time rather than a difference in wall-clock readings.
func (a *Activity) Last() time.Time { return a.origin.Add(time.Duration(a.since.Load())) }
