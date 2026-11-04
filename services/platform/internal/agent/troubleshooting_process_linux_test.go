//go:build linux

package agent

import "testing"

func TestParseProcessStatusHandlesAnyCommandName(t *testing.T) {
	const rest = " S 1 77 88 0 -1 4194304 1 0 0 0 0 0 0 0 20 0 1 0 12345 0 0"
	for _, name := range []string{"(sh)", "(a b)", "(a) Z 9 9 9 (b)", "()"} {
		status, err := parseProcessStatus("42 " + name + rest)
		if err != nil || status.state != "S" || status.session != 88 || status.threads != 1 || status.start != 12345 {
			t.Errorf("%s: %+v %v", name, status, err)
		}
	}
	// A zombie main thread with live threads has not exited.
	if status, err := parseProcessStatus("42 (sh) Z 1 77 88 0 -1 4194304 1 0 0 0 0 0 0 0 20 0 3 0 12345 0 0"); err != nil || status.exited() {
		t.Fatal("live threads treated as exited", err)
	}
	if status, err := parseProcessStatus("42 (sh) Z 1 77 88 0 -1 4194304 1 0 0 0 0 0 0 0 20 0 1 0 12345 0 0"); err != nil || !status.exited() {
		t.Fatal("zombie not treated as exited", err)
	}
	if _, err := parseProcessStatus("42 (sh) S 1"); err == nil {
		t.Fatal("short status accepted")
	}
}
