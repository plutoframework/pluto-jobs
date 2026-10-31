#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go run ./scripts/commentspacing -formatter "$PWD/scripts/golangci.sh" .
root="$PWD"

python3 - <<'PYTHON'
from pathlib import Path

for path in Path('.').rglob('*'):
    if not path.is_file() or '.git' in path.parts:
        continue
    data = path.read_bytes()
    if b'\0' not in data and len(data.splitlines()) > 1000:
        raise SystemExit(f'File exceeds 1,000 lines: {path}')
PYTHON

for module in . stores/postgres; do
  cd "$root/$module"
  "$root/scripts/lint.sh" ./...
  go test ./...
  go vet ./...
  go build ./...
done
