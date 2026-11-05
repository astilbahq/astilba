#!/usr/bin/env bash
# Reproduces the published runs. Usage: run-all.sh <disposable-docker-context> <output-directory> [run-name ...]
set -euo pipefail
context=${1:?disposable Docker context}
mkdir -p "${2:?output directory}"
out=$(cd "$2" && pwd)
shift 2
cd "$(dirname "$0")"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
# measure handles an interrupt and exits 1, so stop the batch here rather than start the next run.
trap 'exit 130' INT TERM
go build -o "$work/fleet-cost" .
measure() { "$work/fleet-cost" measure --docker-context "$context" --out "$out" --warm 2m --window 8m "$@"; }
# Sharing all CPUs raises NumCPU, the only way to enlarge the application's database pool today.
wide=(--app-cpuset 0-11 --db-cpuset 0-11 --generator-cpuset 0-11)
# A throwaway copy of the application with the one-second tick removed. Never committed
# to the application: it bounds what a wake-up could save, without building one.
no_tick=""
prepare_no_tick() {
  [ -n "$no_tick" ] && return 0
  local copy
  copy=$(mktemp -d "$work/no-tick.XXXXXX") || return 1
  mkdir -p "$copy/services" || return 1
  cp -R ../../services/platform "$copy/services/platform" || return 1
  patch -d "$copy" -p1 <no-tick.patch >/dev/null || return 1
  grep -q 'time.NewTicker(time.Hour)' "$copy/services/platform/internal/execution/transport.go" || return 1
  no_tick=$copy
}
run() {
  local count=${1//[!0-9]/}
  case "$1" in
    # PostgreSQL on the VM's disk, used for its memory, since tmpfs pages join its cgroup.
    idle-????) measure --name "$1" --agents "$((10#$count))" ;;
    tmpfs-idle-????) measure --name "$1" --agents "$((10#$count))" --db-tmpfs ;;
    tmpfs-idle-????-pool12) count=${count%12}; measure --name "$1" --agents "$((10#$count))" --db-tmpfs "${wide[@]}" ;;
    tmpfs-idle-????-slow-db) measure --name "$1" --agents "$((10#$count))" --db-tmpfs --db-rtt 500us ;;
    tmpfs-idle-????-no-tick)
      prepare_no_tick || { echo "cannot prepare the no-tick build" >&2; return 1; }
      measure --name "$1" --agents "$((10#$count))" --db-tmpfs --server-source "$no_tick/services/platform" ;;
    tmpfs-active-????) measure --name "$1" --agents "$((10#$count))" --services 5 --deploys-per-day 24 --db-tmpfs ;;
    tmpfs-active-????-pool12) count=${count%12}; measure --name "$1" --agents "$((10#$count))" --services 5 --deploys-per-day 24 --db-tmpfs "${wide[@]}" ;;
    # CPU quotas: vCPUs of the proposed class ÷ its speed ratio × 1.4 typical host slowdown.
    sized-0100) measure --name "$1" --agents 100 --db-tmpfs --db-cpus 0.8 --app-cpus 0.1 "${wide[@]}" ;;
    sized-1000) measure --name "$1" --agents 1000 --db-tmpfs --db-cpus 0.8 --app-cpus 0.4 "${wide[@]}" ;;
    sized-2000) measure --name "$1" --agents 2000 --db-tmpfs --db-cpus 0.8 --app-cpus 0.8 "${wide[@]}" ;;
    *) echo "unknown run $1" >&2; return 1 ;;
  esac
}
if [ $# -eq 0 ]; then
  set -- idle-0000 idle-0100 idle-0500 idle-1000 idle-2000 \
    tmpfs-idle-0100 tmpfs-idle-0500 tmpfs-idle-1000 tmpfs-idle-1500 tmpfs-idle-2000 tmpfs-idle-2000-pool12 \
    tmpfs-idle-1500-slow-db tmpfs-idle-2000-no-tick tmpfs-active-1000 tmpfs-active-1000-pool12 sized-0100 sized-1000 sized-2000
fi
failed=0
for name in "$@"; do
  [ -e "$out/$name.json" ] && { echo "skip $name"; continue; }
  run "$name" || { echo "fleet-cost: run $name failed" >&2; failed=1; }
done
exit "$failed"
