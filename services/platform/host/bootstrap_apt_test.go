package host

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fakeAptGet records each call and fails, as apt-get does, when the source in use is listed
// in FAILING: the host's own sources ("host") or a fallback archive by its URI.
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
for failing in $FAILING; do [[ $failing == "$source" ]] && exit 100; done
exit 0
`

// Runs the served script's own apt functions against a fake apt-get, so which sources it tries,
// and in what order, can't drift from what ships.
func TestBootstrapTriesPinnedArchivesOnlyWhenHostSourcesFail(t *testing.T) {
	script := string(Bootstrap())
	die := regexp.MustCompile(`(?m)^die\(\) .*$`).FindString(script)
	apt := regexp.MustCompile(`(?ms)^ubuntu_keyring=.*?^apt_install\(\) \{.*?^\}$`).FindString(script)
	archives := regexp.MustCompile(`(?m)^ +fallback_archives=\(([^)]*)\) ;;$`).FindAllStringSubmatch(script, -1)
	if die == "" || apt == "" || len(archives) != 2 {
		t.Fatal("the bootstrap no longer defines die, its apt functions and a fallback list per architecture")
	}
	for _, list := range archives {
		for _, archive := range strings.Fields(list[1]) {
			if !regexp.MustCompile(`^https?://[a-z0-9.-]+/[a-z0-9/_-]*ubuntu(-ports)?$`).MatchString(archive) {
				t.Errorf("fallback archive %q is not a plain Ubuntu archive URI", archive)
			}
		}
	}
	amd64 := strings.Fields(archives[0][1])
	for _, test := range []struct {
		name    string
		failing []string
		used    string // The fallback archive the install uses, or empty for the host's sources.
		ok      bool
	}{
		{name: "host sources work", ok: true},
		{name: "host sources fail", failing: []string{"host"}, used: amd64[0], ok: true},
		{name: "host sources and the first archive fail", failing: []string{"host", amd64[0]}, used: amd64[1], ok: true},
		{name: "everything fails", failing: append([]string{"host"}, amd64...)},
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
				`apt_download /tmp/docker.deb nftables=1.0.9-1ubuntu0.1`,
				`apt_install /tmp/docker.deb`,
			}, "\n")
			cmd := exec.Command("bash", "-c", harness)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "LOG="+log, "SAVED="+saved, "FAILING="+strings.Join(test.failing, " "))
			out, err := cmd.CombinedOutput()
			if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 77 {
				t.Skip("needs bash 4.4 or later, as production has")
			}
			if (err == nil) != test.ok {
				t.Fatalf("apt_download ok=%v, want %v:\n%s", err == nil, test.ok, out)
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
			}
			// The host's sources come first, then the pinned archives in order, each only after the one before failed.
			want := []string{"host"}
			if !test.ok || test.used != "" {
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
				if !bytes.Contains(out, []byte("Neither this host's package sources nor any fallback Ubuntu archive")) {
					t.Fatalf("no explanation:\n%s", out)
				}
				return
			}
			install := calls[len(calls)-1]
			if !strings.HasPrefix(install, test.used+" ") && !(test.used == "" && strings.HasPrefix(install, "host ")) || !strings.HasSuffix(install, " install --yes --no-remove --no-install-recommends /tmp/docker.deb") {
				t.Fatalf("installed with %q, want the sources the download used (%q)", install, test.used)
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
			// apt checks the fallback exactly as Ubuntu's own sources are checked, and a fallback's
			// moving suites must be fresh.
			stanzas := strings.Split(strings.TrimSpace(string(sources)), "\n\n")
			wantStanzas := []string{
				"Types: deb\nURIs: " + test.used + "\nSuites: noble\nComponents: main restricted universe multiverse\nSigned-By: " + keyring,
				"Types: deb\nURIs: " + test.used + "\nSuites: noble-updates noble-security\nComponents: main restricted universe multiverse\nSigned-By: " + keyring + "\nValid-Until-Max: 604800",
			}
			if !slices.Equal(stanzas, wantStanzas) {
				t.Fatalf("fallback sources:\n%s\nwant:\n%s", sources, strings.Join(wantStanzas, "\n\n"))
			}
		})
	}
}

// The bootstrap's fallback archives are CI's, less the one only Azure's runners can reach.
func TestBootstrapFallbackArchivesMatchCI(t *testing.T) {
	ci, err := os.ReadFile("../../../scripts/ci-ubuntu-archive.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(Bootstrap())
	for _, arch := range []string{"amd64", "arm64"} {
		ciList := regexp.MustCompile(`(?m)^  ` + arch + `\) archives=\(([^)]*)\) ;;$`).FindSubmatch(ci)
		if ciList == nil {
			t.Fatalf("scripts/ci-ubuntu-archive.sh has no %s archive list", arch)
		}
		want := slices.DeleteFunc(strings.Fields(string(ciList[1])), func(a string) bool { return strings.Contains(a, "azure.") })
		hardware := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
		block := regexp.MustCompile(`(?ms)^  ` + hardware + `\)\n.*?^ +fallback_archives=\(([^)]*)\) ;;$`).FindStringSubmatch(script)
		if block == nil {
			t.Fatalf("the bootstrap has no %s fallback list", arch)
		}
		if got := strings.Fields(block[1]); !slices.Equal(got, want) {
			t.Errorf("%s fallback archives %q, want CI's %q", arch, got, want)
		}
	}
}
