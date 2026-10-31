#!/usr/bin/env bash
set -euo pipefail
task_root=$(cd "$(dirname "$0")/.." && pwd)

# Formatting is checked by commentspacing as a composed policy. Running the
# formatters again as linters would reject required block-start comment spacing.
linters=$(python3 - "$task_root/.golangci.yml" <<'PYTHON'
from pathlib import Path
import re
import sys

text = Path(sys.argv[1]).read_text()
section = re.search(r"(?ms)^linters:\n(.*?)(?=^\S|\Z)", text)
if not section or not re.search(r"^  default: none$", section[1], re.M):
    raise SystemExit("lint.sh requires an explicit linters list and default: none")
enabled = re.search(r"(?m)^  enable:\n((?:    - [a-z0-9_]+\n)+)", section[1])
if not enabled:
    raise SystemExit("Cannot read linters.enable; update lint.sh for this config")
print(",".join(re.findall(r"    - ([a-z0-9_]+)", enabled[1])))
PYTHON
)
exec "$task_root/scripts/golangci.sh" run --enable-only "$linters" "$@"
