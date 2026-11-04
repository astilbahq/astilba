#!/usr/bin/env bash
# Runs selected native agent tests as root inside an explicitly named disposable
# Colima VM. Not a substitute for CI: see docs/continuous-integration.md.
set -euo pipefail

profile="${1:-}"
pattern="${2:-^TestNativeReleaseCommandTransport\$}"

case "$profile" in
  "")
    echo "Set PLAT_AGENT_COLIMA_PROFILE to a disposable Colima profile." >&2
    exit 2
    ;;
  # Colima resolves both names to its default profile.
  default | colima)
    echo "Refusing Colima's default profile: name a disposable one." >&2
    exit 2
    ;;
esac

vm() { colima ssh -p "$profile" -- "$@"; }

machine="$(vm uname -m </dev/null)"
case "$machine" in
  aarch64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *)
    echo "Unsupported VM architecture: $machine" >&2
    exit 1
    ;;
esac

workspace="$(mktemp -d)"
binary=""
cleanup() {
  if [[ -n "$binary" ]] && ! vm rm -f "$binary" </dev/null; then
    echo "Could not remove $binary from Colima profile $profile." >&2
  fi
  rm -rf "$workspace"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$(dirname "$0")/../services/platform"
GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go test -c -o "$workspace/agent.test" ./internal/agent

binary="$(vm mktemp /tmp/astilba-native-agent.XXXXXX </dev/null)"
vm dd of="$binary" status=none <"$workspace/agent.test"
vm chmod 700 "$binary" </dev/null

# The script arrives on stdin, so each value crosses only one shell.
vm bash -s <<EOF 2>&1 | tee "$workspace/test.log"
cd /
exec sudo env ASTILBA_NATIVE_AGENT=1 PLAT_APP_DOCKER_HOST=unix:///var/run/docker.sock $binary -test.v -test.timeout=30m -test.run $(printf %q "$pattern") </dev/null
EOF

# A test binary exits 0 when the pattern selects nothing.
if grep -q '^testing: warning: no tests to run' "$workspace/test.log"; then
  echo "PLAT_AGENT_TESTS selected no tests: $pattern" >&2
  exit 1
fi
