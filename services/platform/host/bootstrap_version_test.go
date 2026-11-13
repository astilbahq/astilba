package host

import (
	"fmt"
	"os"
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
		functions,
		`while read -r a b; do version_compare "$a" "$b"; done`,
	}, "\n")
	cmd := exec.Command("bash", "-c", harness)
	// Run under a locale whose collation isn't byte order, as an operator's sudo often keeps
	// one, to show the functions pin their own.
	cmd.Env = append(os.Environ(), "LC_ALL="+collatingLocale(t))
	cmd.Stdin = strings.NewReader(strings.Join(pairs, "\n") + "\n")
	out, err := cmd.Output()
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

// collatingLocale names an installed locale whose collation isn't byte order, as glibc's
// en_US.UTF-8 isn't, or C.UTF-8 when there is none, which still checks everything but the pinning.
func collatingLocale(t *testing.T) string {
	out, _ := exec.Command("locale", "-a").Output()
	for _, name := range strings.Fields(string(out)) {
		// In byte order "B" sorts before "a".
		cmd := exec.Command("bash", "-c", `[[ a < B ]]`)
		cmd.Env = append(os.Environ(), "LC_ALL="+name)
		if cmd.Run() == nil {
			return name
		}
	}
	t.Log("no installed locale collates other than byte order, so this doesn't show version_compare pins its collation")
	return "C.UTF-8"
}

// The bootstrap's version pattern accepts exactly what agents call a version.
func TestBootstrapVersionPatternMatchesAgents(t *testing.T) {
	pattern := regexp.MustCompile(`(?m)^semver='([^']*)'$`).FindStringSubmatch(string(Bootstrap()))
	if pattern == nil {
		t.Fatal("the bootstrap no longer defines semver")
	}
	inputs := []string{"0.0.0", "1.2.3", "1.2.3-rc.1", "1.2.3-a-b.0", "1.2.3-0a", "01.2.3", "1.02.3", "1.2.3-01", "1.2.3+build", "1.2", "v1.2.3", "1.2.3-", "1.2.3-a..b", "1.2.3 ", "1.2.3-Alpha", "99999999999999999999.0.0"}
	harness := `while IFS= read -r v; do if [[ $v =~ $1 ]]; then echo yes; else echo no; fi; done`
	cmd := exec.Command("bash", "-c", harness, "bash", pattern[1])
	cmd.Stdin = strings.NewReader(strings.Join(inputs, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(out))
	for i, v := range inputs {
		if want := map[bool]string{true: "yes", false: "no"}[agentrelease.ValidVersion(v)]; i >= len(got) || got[i] != want {
			t.Errorf("the bootstrap's pattern and agentrelease.ValidVersion disagree on %q", v)
		}
	}
}
