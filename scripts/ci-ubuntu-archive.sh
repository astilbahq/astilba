#!/usr/bin/env bash
# Points a disposable GitHub-hosted runner's Ubuntu apt sources at the first archive below that
# serves current metadata signed with Ubuntu's archive key: Canonical's own, then independent
# mirrors resolved through other DNS. CI only; a customer's host keeps its own sources.
#
# Only source URIs change. Signed-By and the keyring are left as they are, so apt still verifies
# every index and package against Ubuntu's archive key whichever archive serves them.
set -euo pipefail

if [[ "${GITHUB_ACTIONS:-}" != true || "${RUNNER_ENVIRONMENT:-}" != github-hosted || "$EUID" != 0 ]]; then
  echo 'This rewrites apt sources; run it only as root on a disposable GitHub-hosted runner.' >&2
  exit 1
fi

keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg
max_age_days=7
codename=$(. /etc/os-release && echo "$VERSION_CODENAME")
architecture=$(dpkg --print-architecture)
case "$architecture" in
  amd64) archives=(http://azure.archive.ubuntu.com/ubuntu http://archive.ubuntu.com/ubuntu https://mirrors.edge.kernel.org/ubuntu https://mirrors.mit.edu/ubuntu) ;;
  arm64) archives=(http://ports.ubuntu.com/ubuntu-ports https://mirrors.mit.edu/ubuntu-ports https://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports) ;;
  *) echo "No Ubuntu archive list for $architecture." >&2; exit 1 ;;
esac
# The sources a runner image uses for Ubuntu itself: its mirror list or a Canonical archive.
# Bracket expressions, not backslashes: awk -v would turn `\.` into `.` and `\+` into `+`.
canonical='^(mirror[+]file:/etc/apt/apt-mirrors[.]txt|https?://([a-z0-9-]+[.])*(archive|security|ports)[.]ubuntu[.]com/ubuntu(-ports)?/?)$'

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

# Succeeds when the archive's security suite is signed by Ubuntu's archive key and was published
# within the last week. This screens out unreachable and stale mirrors; apt's own verification
# of what it downloads is what protects the packages.
usable() {
  local archive=$1 suite=$codename-security published age
  rm -f "$scratch/InRelease" "$scratch/verified"
  curl --fail --silent --show-error --location --max-time 20 --output "$scratch/InRelease" "$archive/dists/$suite/InRelease" || return 1
  gpgv --keyring "$keyring" --output "$scratch/verified" "$scratch/InRelease" || return 1
  [[ $(awk '/^Suite: /{print $2; exit}' "$scratch/verified") == "$suite" ]] || { echo "$archive/dists/$suite/InRelease is signed for another suite." >&2; return 1; }
  published=$(awk '/^Date: /{sub(/^Date: /, ""); print; exit}' "$scratch/verified")
  published=$(date --date "$published" +%s) || return 1
  age=$(($(date +%s) - published))
  if ((age < -3600 || age > max_age_days * 86400)); then
    echo "$archive last published $suite $((age / 3600)) hours ago, outside the last $max_age_days days." >&2
    return 1
  fi
}

chosen=
for archive in "${archives[@]}"; do
  echo "Trying $archive"
  if usable "$archive"; then
    chosen=$archive
    break
  fi
done
[[ -n "$chosen" ]] || { echo "::error::No Ubuntu archive in the list served current, correctly signed $codename-security metadata."; exit 1; }
if [[ ! "$chosen" =~ $canonical ]]; then
  echo "::warning::Canonical's archive did not answer. Ubuntu packages for this job come from $chosen, verified against Ubuntu's archive key."
fi

mapfile -d '' sources < <(find /etc/apt/sources.list /etc/apt/sources.list.d -type f \( -name '*.list' -o -name '*.sources' \) -print0)
replaced=0
for file in "${sources[@]}"; do
  # Rewrites only the URI fields of active deb822 stanzas and one-line entries.
  awk -v pattern="$canonical" -v chosen="$chosen" -v counted="$scratch/count" '
    function swap(i) { if (i <= NF && $i ~ pattern) { $i = chosen; swapped++ } }
    /^[[:space:]]*#/ { print; next }
    /^URIs:/ { for (i = 2; i <= NF; i++) swap(i); print; next }
    /^[[:space:]]*deb(-src)?[[:space:]]/ {
      i = 2
      if ($2 ~ /^\[/) { while (i <= NF && $i !~ /\]$/) i++; i++ }
      swap(i); print; next
    }
    { print }
    END { print swapped + 0 > counted }
  ' "$file" > "$scratch/source"
  count=$(cat "$scratch/count")
  if ((count > 0)); then
    cat "$scratch/source" > "$file"
    replaced=$((replaced + count))
    echo "Ubuntu sources in $file:"
    cat "$file"
  fi
done
((replaced > 0)) || { echo '::error::Found no Ubuntu archive source to point at the chosen archive; the runner image has changed.'; exit 1; }

# apt's default is 3; a few more cover brief resets from whichever archive serves this job.
printf 'Acquire::Retries "5";\n' > /etc/apt/apt.conf.d/80-ci-retries
