#!/bin/sh
# Runs a hook script under the repository's pinned Node.
version=24.18.1
script="$(dirname "$0")/$1"
if command -v fnm >/dev/null 2>&1 && fnm exec --using="$version" -- node --version >/dev/null 2>&1; then
  exec fnm exec --using="$version" -- node "$script"
fi
if [ "$(node --version 2>/dev/null)" = "v$version" ]; then
  exec node "$script"
fi
# Every edit would repeat the notice, so only session start and stop report it.
if [ "$1" != after-edit.mjs ]; then
  printf '{"systemMessage":"Project hooks did not run: they need Node %s. Install fnm and run: fnm install %s"}\n' "$version" "$version"
fi
