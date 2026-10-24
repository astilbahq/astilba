#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
: "${PLAT2_DOCKER_HOST:?Set PLAT2_DOCKER_HOST to a disposable local Docker Unix socket.}"
case "$PLAT2_DOCKER_HOST" in
  unix://*) ;;
  *) echo "Only an explicit local Docker Unix socket is supported." >&2; exit 1 ;;
esac

if [[ "$(go env GOVERSION)" != "go1.27.3" ]]; then
  echo "This experiment is pinned to Go 1.27.3." >&2
  exit 1
fi
unformatted="$(gofmt -l .)"
if [[ -n "$unformatted" ]]; then
  echo "$unformatted" >&2
  exit 1
fi
go mod verify
go vet ./...
go test -race -count=1 -timeout=10m -v ./...
