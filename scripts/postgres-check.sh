#!/usr/bin/env bash
# All database work goes to a fresh, disposable container and isolated schemas.
set -euo pipefail
cd "$(dirname "$0")/.."
container="pluto-jobs-check-$$"
result=$(mktemp)
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -f "$result"
}
trap cleanup EXIT
docker info >/dev/null
docker run -d --rm --name "$container" \
  -e POSTGRES_USER=pluto -e POSTGRES_PASSWORD=local_only \
  -e POSTGRES_DB=pluto_jobs -p 127.0.0.1::5432 postgres:17.6-alpine >/dev/null
ready=false
for attempt in $(seq 1 40); do
  if docker exec "$container" pg_isready -U pluto -d pluto_jobs >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  docker logs "$container"
  exit 1
fi
port=$(docker port "$container" 5432/tcp | awk -F: '{print $NF}')
export PLUTO_JOBS_TEST_DATABASE_URL="postgres://pluto:local_only@127.0.0.1:${port}/pluto_jobs?sslmode=disable"
export PLUTO_JOBS_REQUIRE_DATABASE=1
(cd stores/postgres && go test -race -count=1 -json ./...) >"$result" || {
  cat "$result"
  exit 1
}
python3 - "$result" <<'PY'
import json,sys
from pathlib import Path
records=[json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines()]
expected={'TestTransactionAtomicity','TestConcurrentClaimsExcludeOtherWorkers',
          'TestLeaseExpiryFencesStaleOwners','TestDelayRetryAndCrashExhaustion',
          'TestPersistenceGuardsAndInvalidStoredWork','TestInsertCommitsOrderAndJobTogether'}
passed={record.get('Test') for record in records if record.get('Action')=='pass'}
skipped=[record.get('Test') for record in records if record.get('Action')=='skip' and record.get('Test')]
if skipped or not expected<=passed:
    raise SystemExit(f'Required Jobs SQL evidence missing: skipped={skipped}, absent={expected-passed}')
print('Required Jobs PostgreSQL cases executed: '+', '.join(sorted(expected)))
PY
python3 scripts/mutation-smoke.py
