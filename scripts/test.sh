#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
for module in . stores/postgres; do
  cd "$root/$module"
  go test "$@" ./...
done
