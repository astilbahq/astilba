package terminal

import (
	"strings"
	"testing"
	"time"
)

// Idle time must be measured on the monotonic clock. A time from Unix nanoseconds has
// only a wall-clock reading, so a clock step would move the limit: Go subtracts monotonic
// readings only when both times carry one, and String shows one as "m=".
func TestActivityKeepsTheMonotonicReading(t *testing.T) {
	a := NewActivity()
	if last := a.Last(); !strings.Contains(last.String(), " m=") {
		t.Fatalf("start of the session %s has no monotonic reading", last)
	}
	time.Sleep(10 * time.Millisecond)
	typed := a.Touch()
	last := a.Last()
	if !strings.Contains(last.String(), " m=") {
		t.Fatalf("last input %s has no monotonic reading", last)
	}
	if !last.Equal(typed) {
		t.Fatalf("last input %s, want %s", last, typed)
	}
	if idle := time.Since(last); idle < 0 || idle > time.Second {
		t.Fatalf("idle for %s straight after input", idle)
	}
}
