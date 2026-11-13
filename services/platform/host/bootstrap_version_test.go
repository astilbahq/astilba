package host

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
)

// The bootstrap compares a release's minimum version with the operator's in bash. It must order
// versions exactly as agents do, or it would accept a release an agent's floor refuses, or the
// reverse.
func TestBootstrapOrdersVersionsAsAgentsDo(t *testing.T) {
	script := string(Bootstrap())
	functions := regexp.MustCompile(`(?ms)^number_compare\(\) \{.*?^\}$.*?^version_compare\(\) \{.*?^\}$`).FindString(script)
	if functions == "" {
		t.Fatal("the bootstrap no longer defines number_compare and version_compare")
	}
	versions := []string{
		// Semantic versioning's own example order, then edge cases.
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
		"0.0.0", "0.0.1-development", "0.3.0", "0.3.0-rc.1", "0.9.9", "0.10.0", "2.0.0", "10.0.0",
		"1.0.0-0", "1.0.0-1", "1.0.0-0a", "1.0.0-a-b", "1.0.0-Alpha", "1.0.0-alpha.0", "1.0.0-alpha.alpha.1",
		"99999999999999999999.0.0", "1.0.0-99999999999999999999", "1.0.0-99999999999999999998",
	}
	var pairs, want []string
	for _, a := range versions {
		if !agentrelease.ValidVersion(a) {
			t.Fatalf("%s is not a version", a)
		}
		for _, b := range versions {
			pairs = append(pairs, a+" "+b)
			want = append(want, fmt.Sprint(agentrelease.CompareVersions(a, b)))
		}
	}
	harness := strings.Join([]string{
		"set -euo pipefail",
		"((BASH_VERSINFO[0] * 100 + BASH_VERSINFO[1] >= 404)) || exit 77",
		functions,
		`while read -r a b; do version_compare "$a" "$b"; done`,
	}, "\n")
	cmd := exec.Command("bash", "-c", harness)
	cmd.Stdin = strings.NewReader(strings.Join(pairs, "\n") + "\n")
	out, err := cmd.Output()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 77 {
		t.Skip("needs bash 4.4 or later, as production has")
	}
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(out))
	if len(got) != len(want) {
		t.Fatalf("%d results for %d pairs", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("version_compare %s = %s, agents order it %s", pairs[i], got[i], want[i])
		}
	}
}
