#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"

for module in . stores/postgres; do
  cd "$root/$module"
  "$root/scripts/golangci.sh" fmt ./...
  "$root/scripts/golangci.sh" run --enable-only wsl_v5 --fix ./...
  "$root/scripts/golangci.sh" fmt ./...
done
