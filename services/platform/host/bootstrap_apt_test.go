package host

import (
	"bytes"
	"cmp"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fakeAptGet records each call as the sources it used ("host", or a fallback archive's URI),
// whether its package lists started from the host's, and its arguments.
//   - An update fails, as apt-get's does, when its sources are listed in FAILING or PARTIAL.
//     Otherwise, and for PARTIAL, it writes the release files of noble and its updates and
//     security suites: current, unless the sources are in STALE (security a month old) or
//     PREPOCKET (a pre-release noble).
//   - A download fails after a FAILING update, since apt then has no lists to resolve from, and
//     always when DOWNLOAD_FAILS is set.
//   - Unless NOTHING_NEEDED is set, a package needs downloading.
//   - An archive listed in SUBSTITUTED serves -proposed as -security.
const fakeAptGet = `#!/usr/bin/env bash
source=host
for argument in "$@"; do
  case $argument in
    Dir::Etc::SourceParts=*)
      file=${argument#*=}/ubuntu.sources
      source=$(awk '/^URIs:/ { print $2; exit }' "$file")
      cp "$file" "$SAVED/$(printf '%s' "$source" | tr -c 'A-Za-z0-9' _)" ;;
    Dir::State::Lists=*) lists=${argument#*=} ;;
  esac
done
seeded=unseeded
[[ ! -e $lists/host_InRelease ]] || seeded=seeded
tag=$(printf '%s' "$source" | tr -c 'A-Za-z0-9' _)
listed() { for listed in $1; do [[ $listed == "$source" ]] && return 0; done; return 1; }
printf '%s %s %s\n' "$source" "$seeded" "$*" >> "$LOG"
case " $* " in
  *" indextargets "*)
    for release in noble noble-updates noble-security; do
      suite=$release
      [[ $release != noble-security ]] || ! listed "$SUBSTITUTED" || suite=noble-proposed
      echo "$release $suite noble Ubuntu ${lists}/${tag}_dists_${release}_main_binary-amd64_Packages.lz4 main/binary-amd64/Packages"
    done ;;
  *" --print-uris "*)
    printf "'file:/tmp/docker.deb' docker.deb 1 \n"
    [[ -n $NOTHING_NEEDED ]] || printf "'http://archive.test/nftables.deb' nftables.deb 1 \n" ;;
  *" update "*)
    listed "$FAILING" && { touch "$SAVED/unrefreshed-$tag"; exit 100; }
    pocket="Thu, 25 Apr 2024 15:10:33 UTC" security=$(date -u -R)
    ! listed "$PREPOCKET" || pocket="Wed, 24 Apr 2024 15:10:33 UTC"
    ! listed "$STALE" || security=$(date -u -R --date '30 days ago')
    printf 'Origin: Ubuntu\nDate: %s\n' "$pocket" > "$lists/${tag}_dists_noble_InRelease"
    printf 'Origin: Ubuntu\nDate: %s\n' "$(date -u -R)" > "$lists/${tag}_dists_noble-updates_InRelease"
    printf 'Origin: Ubuntu\nDate: %s\n' "$security" > "$lists/${tag}_dists_noble-security_InRelease"
    ! listed "$PARTIAL" || exit 100 ;;
  *" --download-only "*)
    [[ -z $DOWNLOAD_FAILS && ! -e "$SAVED/unrefreshed-$tag" ]] || exit 100 ;;
esac
exit 0
`

// Runs the served script's own apt functions against a fake apt-get, so which sources it tries,
// and in what order, can't drift from what ships.
func TestBootstrapTriesPinnedArchivesOnlyWhenHostSourcesFail(t *testing.T) {
	script := string(Bootstrap())
	die := regexp.MustCompile(`(?m)^die\(\) .*$`).FindString(script)
	apt := regexp.MustCompile(`(?ms)^ubuntu_keyring=.*?^apt_download\(\) \{.*?^\}$`).FindString(script)
	install := regexp.MustCompile(`(?m)^ +(DEBIAN_FRONTEND=noninteractive apt-get "\$\{apt_options\[@\]\}" .*install .*"\$\{apt_packages\[@\]\}")$`).FindStringSubmatch(script)
	archives := regexp.MustCompile(`(?m)^ +fallback_archives=\(([^)]*)\) ;;$`).FindAllStringSubmatch(script, -1)
	if die == "" || apt == "" || install == nil || len(archives) != 2 {
		t.Fatal("the bootstrap no longer defines die, its apt functions, the runtime install and a fallback list per architecture")
	}
	amd64 := strings.Fields(archives[0][1])
	for _, test := range []struct {
		name          string
		failing       []string // Sources whose update fails, leaving nothing to download from.
		partial       []string // Sources whose update fails for an unrelated index.
		substituted   []string // Archives that serve another suite under a requested one's name.
		stale         []string // Sources whose security index is a month old.
		prePocket     []string // Sources that replay a pre-release noble.
		downloadFails bool
		nothingNeeded bool
		used          string // The fallback archive the install uses, or empty for the host's sources.
		ok            bool
		output        string
	}{
		{name: "host sources work", ok: true},
		// A host that has every dependency needs no index, so an outage doesn't hold it up.
		{name: "nothing to download", nothingNeeded: true, failing: []string{"host"}, ok: true},
		// A dead third-party source must not send a host that can still get its packages elsewhere.
		{name: "an unrelated host source fails", partial: []string{"host"}, ok: true, output: "Some of this host's package indexes failed to refresh"},
		{name: "host sources fail", failing: []string{"host"}, used: amd64[0], ok: true},
		{name: "host sources and the first archive fail", failing: []string{"host", amd64[0]}, used: amd64[1], ok: true},
		{name: "the first archive serves another suite", failing: []string{"host"}, substituted: []string{amd64[0]}, used: amd64[1], ok: true, output: "An index for noble-security is suite noble-proposed"},
		// A source that serves an old security index, as a stale mirror or a replay can, isn't used.
		{name: "the host's security index is stale", stale: []string{"host"}, used: amd64[0], ok: true, output: "This host's Ubuntu package indexes are not current."},
		{name: "the first archive's security index is stale", failing: []string{"host"}, stale: []string{amd64[0]}, used: amd64[1], ok: true, output: "The noble-security index was not published in the last week."},
		{name: "the first archive replays a pre-release noble", failing: []string{"host"}, prePocket: []string{amd64[0]}, used: amd64[1], ok: true, output: "The noble index predates Ubuntu 24.04."},
		{name: "everything fails", failing: append([]string{"host"}, amd64...), output: "Neither this host's package sources nor any fallback Ubuntu archive"},
		// A download that fails although every index refreshed, such as a package apt can't
		// resolve, would fail the same way from every archive.
		{name: "packages can't be resolved", downloadFails: true, output: "apt could not resolve or download the Ubuntu packages"},
		// A fallback whose indexes refresh may still fail to serve a package, as a mirror part way
		// through a sync can, so the next one is tried.
		{name: "no fallback serves the packages", failing: []string{"host"}, downloadFails: true, output: "Neither this host's package sources nor any fallback Ubuntu archive"},
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
			lists := filepath.Join(dir, "lists")
			if err := os.MkdirAll(filepath.Join(lists, "partial"), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"host_InRelease", "lock"} {
				if err := os.WriteFile(filepath.Join(lists, name), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(dir, "log")
			harness := strings.Join([]string{
				"set -euo pipefail",
				// Production runs Ubuntu 24.04's bash 5.2; bash before 4.4 can't expand an empty array under set -u.
				"((BASH_VERSINFO[0] * 100 + BASH_VERSINFO[1] >= 404)) || exit 77",
				// Production runs GNU date, which reads the dates in release files.
				"date --date @0 >/dev/null 2>&1 || exit 77",
				die, apt,
				// Without root, apt's own user can't be given its directories, and macOS has no timeout.
				`install() { local a=(); while (($#)); do case $1 in -o|-g) shift 2 ;; *) a+=("$1"); shift ;; esac; done; command install "${a[@]}"; }`,
				`command -v timeout >/dev/null || timeout() { shift 3; "$@"; }`,
				"VERSION_CODENAME=noble VERSION_ID=24.04",
				"fallback_archives=(" + archives[0][1] + ")",
				"ubuntu_keyring=" + keyring,
				"host_lists=" + lists,
				`trap 'rm -rf ${apt_root:+"$apt_root"}' EXIT`,
				`apt_packages=(/tmp/docker.deb nftables=1.0.9-1ubuntu0.1)`,
				`apt_download "${apt_packages[@]}"`,
				install[1],
			}, "\n")
			flag := func(set bool) string {
				if set {
					return "1"
				}
				return ""
			}
			cmd := exec.Command("bash", "-c", harness)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "LOG="+log, "SAVED="+saved, "FAILING="+strings.Join(test.failing, " "), "PARTIAL="+strings.Join(test.partial, " "), "SUBSTITUTED="+strings.Join(test.substituted, " "), "STALE="+strings.Join(test.stale, " "), "PREPOCKET="+strings.Join(test.prePocket, " "), "DOWNLOAD_FAILS="+flag(test.downloadFails), "NOTHING_NEEDED="+flag(test.nothingNeeded))
			out, err := cmd.CombinedOutput()
			if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 77 {
				t.Skip("needs bash 4.4 or later and GNU date, as production has")
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
				source, rest, _ := strings.Cut(call, " ")
				seeded, args, _ := strings.Cut(rest, " ")
				// apt refuses an index older than one it has, so the host's sources start from its lists.
				if (seeded == "seeded") != (source == "host") {
					t.Errorf("a call with %s lists: %s", seeded, call)
				}
				if strings.HasSuffix(args, " update") {
					if !strings.Contains(args, "-o APT::Update::Error-Mode=any update") {
						t.Errorf("an update that ignores failed indexes: %s", call)
					}
					tried = append(tried, source)
				}
				// Downloading and installing wait for dpkg's lock, which a fresh VM's
				// unattended-upgrades often holds.
				if strings.Contains(args, " install ") && !strings.Contains(args, " --print-uris ") && !strings.Contains(args, "-o DPkg::Lock::Timeout=300 install ") {
					t.Errorf("a download or install that doesn't wait for dpkg's lock: %s", call)
				}
				// Every call keeps its package lists and downloads out of the host's own.
				if !strings.Contains(args, "-o Dir::State::Lists=/tmp/astilba-bootstrap-apt.") || !strings.Contains(args, "-o Dir::Cache=/tmp/astilba-bootstrap-apt.") {
					t.Errorf("a call that uses the host's package lists or cache: %s", call)
				}
			}
			// The host's sources come first, then the pinned archives in order, each only after the one before failed.
			want := []string{"host"}
			if test.nothingNeeded {
				want = nil
			} else if len(test.failing) > 0 || len(test.stale) > 0 {
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
			if source, _, _ := strings.Cut(last, " "); source != cmp.Or(test.used, "host") || !strings.HasSuffix(last, " install --yes --no-remove --no-install-recommends /tmp/docker.deb nftables=1.0.9-1ubuntu0.1") {
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
			// apt checks a fallback against Ubuntu's archive key alone, however this host's apt
			// configuration relaxes checks.
			fields := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(sources)), "\n") {
				key, value, _ := strings.Cut(line, ": ")
				fields[key] = value
			}
			wantFields := map[string]string{"Types": "deb", "URIs": test.used, "Suites": "noble noble-updates noble-security", "Components": "main", "Signed-By": keyring, "Allow-Insecure": "no", "Allow-Weak": "no", "Allow-Downgrade-To-Insecure": "no", "Check-Date": "yes"}
			if !maps.Equal(fields, wantFields) {
				t.Errorf("fallback sources:\n%s", sources)
			}
		})
	}
}
