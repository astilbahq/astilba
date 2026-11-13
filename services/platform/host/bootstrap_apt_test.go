package host

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fakeAptGet records each call as the sources it used ("host", or a fallback archive's URI)
// followed by its arguments. An update fails, as apt-get's does, when those sources are listed in
// FAILING or PARTIAL. A download fails after a FAILING update, since apt then has no lists to
// resolve from, and always when DOWNLOAD_FAILS is set.
const fakeAptGet = `#!/usr/bin/env bash
source=host
for argument in "$@"; do
  case $argument in
    Dir::Etc::SourceParts=*)
      file=${argument#*=}/ubuntu.sources
      source=$(awk '/^URIs:/ { print $2; exit }' "$file")
      cp "$file" "$SAVED/$(printf '%s' "$source" | tr -c 'A-Za-z0-9' _)" ;;
  esac
done
printf '%s %s\n' "$source" "$*" >> "$LOG"
case " $* " in
  *" update "*)
    for failing in $FAILING; do [[ $failing == "$source" ]] && { touch "$SAVED/unrefreshed-${source//[^A-Za-z0-9]/_}"; exit 100; }; done
    for failing in $PARTIAL; do [[ $failing == "$source" ]] && exit 100; done ;;
  *" --download-only "*)
    [[ -z $DOWNLOAD_FAILS && ! -e "$SAVED/unrefreshed-${source//[^A-Za-z0-9]/_}" ]] || exit 100 ;;
esac
exit 0
`

// Runs the served script's own apt functions against a fake apt-get, so which sources it tries,
// and in what order, can't drift from what ships.
func TestBootstrapTriesPinnedArchivesOnlyWhenHostSourcesFail(t *testing.T) {
	script := string(Bootstrap())
	die := regexp.MustCompile(`(?m)^die\(\) .*$`).FindString(script)
	apt := regexp.MustCompile(`(?ms)^ubuntu_keyring=.*?^apt_download\(\) \{.*?^\}$`).FindString(script)
	install := regexp.MustCompile(`(?m)^ +(DEBIAN_FRONTEND=noninteractive apt-get "\$\{apt_options\[@\]\}" install .*"\$\{packages\[@\]\}")$`).FindStringSubmatch(script)
	archives := regexp.MustCompile(`(?m)^ +fallback_archives=\(([^)]*)\) ;;$`).FindAllStringSubmatch(script, -1)
	if die == "" || apt == "" || install == nil || len(archives) != 2 {
		t.Fatal("the bootstrap no longer defines die, its apt functions, the runtime install and a fallback list per architecture")
	}
	amd64 := strings.Fields(archives[0][1])
	for _, test := range []struct {
		name          string
		failing       []string // Sources whose update fails, leaving nothing to download from.
		partial       []string // Sources whose update fails for an unrelated index.
		downloadFails bool
		used          string // The fallback archive the install uses, or empty for the host's sources.
		ok            bool
		output        string
	}{
		{name: "host sources work", ok: true},
		// A dead third-party source must not send a host that can still get its packages elsewhere.
		{name: "an unrelated host source fails", partial: []string{"host"}, ok: true, output: "Some of this host's package indexes failed to refresh"},
		{name: "host sources fail", failing: []string{"host"}, used: amd64[0], ok: true},
		{name: "host sources and the first archive fail", failing: []string{"host", amd64[0]}, used: amd64[1], ok: true},
		{name: "everything fails", failing: append([]string{"host"}, amd64...), output: "Neither this host's package sources nor any fallback Ubuntu archive"},
		// A download that fails although every index refreshed, such as a package apt can't
		// resolve, would fail the same way from every archive.
		{name: "packages can't be resolved", downloadFails: true, output: "apt could not resolve or download the Ubuntu packages"},
		{name: "a fallback's packages can't be resolved", failing: []string{"host"}, downloadFails: true, used: amd64[0], output: "apt could not resolve or download the Ubuntu packages"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			bin, saved := filepath.Join(dir, "bin"), filepath.Join(dir, "saved")
			for _, d := range []string{bin, saved} {
				if err := os.Mkdir(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "apt-get"), []byte(fakeAptGet), 0o700); err != nil {
				t.Fatal(err)
			}
			keyring := filepath.Join(dir, "ubuntu-archive-keyring.gpg")
			if err := os.WriteFile(keyring, []byte("key"), 0o600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "log")
			harness := strings.Join([]string{
				"set -euo pipefail",
				// Production runs Ubuntu 24.04's bash 5.2; bash before 4.4 can't expand an empty array under set -u.
				"((BASH_VERSINFO[0] * 100 + BASH_VERSINFO[1] >= 404)) || exit 77",
				die, apt,
				// Without root, apt's own user can't be given its directories, and macOS has no timeout.
				`install() { local a=(); while (($#)); do case $1 in -o|-g) shift 2 ;; *) a+=("$1"); shift ;; esac; done; command install "${a[@]}"; }`,
				`command -v timeout >/dev/null || timeout() { shift 3; "$@"; }`,
				"VERSION_CODENAME=noble",
				"fallback_archives=(" + archives[0][1] + ")",
				"ubuntu_keyring=" + keyring,
				`trap 'rm -rf ${apt_root:+"$apt_root"}' EXIT`,
				`packages=(/tmp/docker.deb)`,
				`apt_download "${packages[@]}" nftables=1.0.9-1ubuntu0.1`,
				install[1],
			}, "\n")
			downloadFails := ""
			if test.downloadFails {
				downloadFails = "1"
			}
			cmd := exec.Command("bash", "-c", harness)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "LOG="+log, "SAVED="+saved, "FAILING="+strings.Join(test.failing, " "), "PARTIAL="+strings.Join(test.partial, " "), "DOWNLOAD_FAILS="+downloadFails)
			out, err := cmd.CombinedOutput()
			if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 77 {
				t.Skip("needs bash 4.4 or later, as production has")
			}
			if (err == nil) != test.ok || !bytes.Contains(out, []byte(test.output)) {
				t.Fatalf("apt_download ok=%v, want %v and %q:\n%s", err == nil, test.ok, test.output, out)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
			var tried []string
			for _, call := range calls {
				source, args, _ := strings.Cut(call, " ")
				if strings.HasSuffix(args, " update") {
					if !strings.Contains(args, "-o APT::Update::Error-Mode=any update") {
						t.Errorf("an update that ignores failed indexes: %s", call)
					}
					tried = append(tried, source)
				}
				// Every call keeps its package lists and downloads out of the host's own.
				if !strings.Contains(args, "-o Dir::State::Lists=/tmp/astilba-bootstrap-apt.") || !strings.Contains(args, "-o Dir::Cache=/tmp/astilba-bootstrap-apt.") {
					t.Errorf("a call that uses the host's package lists or cache: %s", call)
				}
			}
			// The host's sources come first, then the pinned archives in order, each only after the one before failed.
			want := []string{"host"}
			if len(test.failing) > 0 {
				for _, archive := range amd64 {
					want = append(want, archive)
					if archive == test.used {
						break
					}
				}
			}
			if !slices.Equal(tried, want) {
				t.Fatalf("tried %q, want %q:\n%s", tried, want, out)
			}
			if !test.ok {
				return
			}
			last := calls[len(calls)-1]
			if source, _, _ := strings.Cut(last, " "); source != cmp.Or(test.used, "host") || !strings.HasSuffix(last, " install --yes --no-remove --no-install-recommends /tmp/docker.deb") {
				t.Fatalf("installed with %q, want the sources the download used (%q)", last, test.used)
			}
			if test.used == "" {
				if bytes.Contains(out, []byte("Trying")) {
					t.Fatalf("a fallback was announced although the host's sources worked:\n%s", out)
				}
				return
			}
			if !bytes.Contains(out, []byte("Ubuntu packages for this run come from "+test.used+".")) {
				t.Fatalf("the archive used is not named:\n%s", out)
			}
			sources, err := os.ReadFile(filepath.Join(saved, regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(test.used, "_")))
			if err != nil {
				t.Fatal(err)
			}
			// apt checks a fallback against Ubuntu's archive key alone, and its moving suites must be fresh.
			for _, stanza := range strings.Split(strings.TrimSpace(string(sources)), "\n\n") {
				fields := map[string]string{}
				for _, line := range strings.Split(stanza, "\n") {
					key, value, _ := strings.Cut(line, ": ")
					fields[key] = value
				}
				moving := strings.Contains(fields["Suites"], "-")
				if fields["URIs"] != test.used || fields["Signed-By"] != keyring || moving != (fields["Valid-Until-Max"] == "604800") || len(fields) != 5+map[bool]int{true: 1}[moving] {
					t.Errorf("fallback stanza:\n%s", stanza)
				}
			}
		})
	}
}
