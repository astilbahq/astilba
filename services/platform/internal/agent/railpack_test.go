package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Runs the real download script with wget and sleep replaced, so an outage is simulated
// without a network or real waits.
func TestRailpackMiseDownloadOutlastsAnOutage(t *testing.T) {
	stubs := `sleep() { echo "$1" >> "$SLEPT"; }
wget() {
  n=$(($(cat "$COUNT" 2>/dev/null || echo 0) + 1))
  echo "$n" > "$COUNT"
  [ "$n" -gt "$FAILURES" ]
}
`
	run := func(t *testing.T, failures int) (ok bool, attempts string, slept int) {
		t.Helper()
		dir := t.TempDir()
		command := exec.Command("sh", "-ec", stubs+railpackMiseDownload, "--", "https://example.test/mise.tgz")
		command.Env = append(os.Environ(), "COUNT="+filepath.Join(dir, "count"), "SLEPT="+filepath.Join(dir, "slept"), "FAILURES="+strconv.Itoa(failures))
		err := command.Run()
		count, _ := os.ReadFile(filepath.Join(dir, "count"))
		delays, _ := os.ReadFile(filepath.Join(dir, "slept"))
		for _, delay := range strings.Fields(string(delays)) {
			seconds, convErr := strconv.Atoi(delay)
			if convErr != nil {
				t.Fatal(convErr)
			}
			slept += seconds
		}
		return err == nil, strings.TrimSpace(string(count)), slept
	}
	t.Run("the host recovers", func(t *testing.T) {
		if ok, attempts, _ := run(t, 3); !ok || attempts != "4" {
			t.Fatalf("ok=%v after %s attempts, want success on the 4th", ok, attempts)
		}
	})
	t.Run("the host stays down", func(t *testing.T) {
		if ok, _, slept := run(t, 1000); ok || slept < 240 {
			t.Fatalf("ok=%v after waiting %d s, want failure after at least four minutes", ok, slept)
		}
	})
}
