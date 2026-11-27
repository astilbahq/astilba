package lanebudget

import (
	"strings"
	"testing"
	"time"
)

const name = "github.com/astilbahq/astilba/services/platform/internal/agent"

func env(values ...string) func(string) string {
	set := map[string]string{}
	for i := 0; i < len(values); i += 2 {
		set[values[i]] = values[i+1]
	}
	return func(key string) string { return set[key] }
}

func TestWarningJudgesEachLaneByItsOwnBudget(t *testing.T) {
	actions := env("GITHUB_ACTIONS", "true")
	native := env("GITHUB_ACTIONS", "true", "ASTILBA_NATIVE_AGENT", "1")
	for _, test := range []struct {
		label   string
		took    time.Duration
		timeout time.Duration
		getenv  func(string) string
		want    string
	}{
		{"a race lane within its budget", RaceBudget, 15 * time.Minute, actions, ""},
		{"a race lane past its budget", RaceBudget + time.Second, 15 * time.Minute, actions, "::warning title=Go race lane past its budget::" + name + " took 601s against a 600s budget, and go test stops at 900s. See docs/continuous-integration.md."},
		{"a race lane without a timeout", RaceBudget + time.Second, 0, actions, "::warning title=Go race lane past its budget::" + name + " took 601s against a 600s budget, and go test sets no timeout. See docs/continuous-integration.md."},
		{"a native lane within its share", 18 * time.Minute, 30 * time.Minute, native, ""},
		{"a native lane past its share", 18*time.Minute + time.Second, 30 * time.Minute, native, "::warning title=Native lane past its budget::" + name + " took 1081s against a 1080s budget, and go test stops at 1800s. See docs/continuous-integration.md."},
		{"a native lane under the race budget", 4 * time.Minute, 5 * time.Minute, native, "::warning title=Native lane past its budget::" + name + " took 240s against a 180s budget, and go test stops at 300s. See docs/continuous-integration.md."},
		{"a native lane without a timeout", time.Hour, 0, native, ""},
		{"a run outside GitHub Actions", time.Hour, 15 * time.Minute, env("ASTILBA_NATIVE_AGENT", "1"), ""},
		{"a run that only names GitHub Actions", time.Hour, 15 * time.Minute, env("GITHUB_ACTIONS", "1"), ""},
	} {
		if got := warning(name, test.took, test.timeout, test.getenv); got != test.want {
			t.Errorf("%s: got %q, want %q", test.label, got, test.want)
		}
	}
}

// start stands in for Start, whose caller's package is the one timed.
func start() string { return callerPackage() }

func TestCallerPackageIsTheTestMainsImportPath(t *testing.T) {
	if got := start(); got != "github.com/astilbahq/astilba/services/platform/internal/lanebudget" {
		t.Fatalf("got %q", got)
	}
}

// Start's package took an hour, past any budget, and prints what it would warn.
func timedRun(t *testing.T) string {
	t.Helper()
	var output strings.Builder
	clock, printed := now, printLine
	t.Cleanup(func() { now, printLine = clock, printed })
	began := time.Now()
	now = func() time.Time { return began }
	printLine = func(line string) { output.WriteString(line) }
	done := Start()
	now = func() time.Time { return began.Add(time.Hour) }
	done()
	return output.String()
}

func TestARunOfTheSameBinaryIsNotTimedAgain(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("ASTILBA_NATIVE_AGENT", "")
	t.Setenv(timedKey, "")
	if !strings.HasPrefix(timedRun(t), "::warning title=Go race lane past its budget::github.com/astilbahq/astilba/services/platform/internal/lanebudget took 3600s") {
		t.Fatal("the test binary's own run did not warn")
	}
	// A helper the test runs inherits the environment its parent's Start marked.
	if output := timedRun(t); output != "" {
		t.Fatalf("a helper process printed %q", output)
	}
}
