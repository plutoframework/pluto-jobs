#!/usr/bin/env bash
set -euo pipefail
task_root=$(cd "$(dirname "$0")/.." && pwd)
version=$(python3 - "$task_root/mise.toml" <<'PYTHON'
import sys
import tomllib
from pathlib import Path
print(tomllib.loads(Path(sys.argv[1]).read_text())['tools']['golangci-lint'])
PYTHON
)
if command -v golangci-lint >/dev/null 2>&1; then
  installed=$(golangci-lint version --short)
  if [[ "$installed" == "$version" ]]; then
    exec golangci-lint "$@" --config "$task_root/.golangci.yml"
  fi
fi
exec mise exec "golangci-lint@$version" -- golangci-lint "$@" --config "$task_root/.golangci.yml"
