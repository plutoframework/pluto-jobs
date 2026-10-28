# Initial implementation validation

Validated locally on macOS arm64 with Go 1.25.5, golangci-lint 2.15.0,
sqlc 1.30.0 and a newly created disposable Postgres 17 container. No existing
application database or sibling source files were changed.

Commands and results (PLUTO_JOBS_TEST_DATABASE_URL set to that disposable DB):

- `./scripts/fmt.sh`: passed; gci, gofumpt, golines and expanded wsl_v5 fixes.
- `./scripts/check.sh`: passed for both modules; file-size guard, formatting
  diff, govet/ineffassign/staticcheck/unused/wsl_v5, tests, vet and build.
- `./scripts/test.sh -race -count=1`: passed for both modules, including real
  Postgres tests. No SQL tests skipped in this run.
- `./scripts/fuzz.sh`: passed, five seconds, two workers, 332,709 executions.
- `python3 scripts/mutation-smoke.py`: six killed, zero survived, zero build
  failures, zero timeouts, zero skipped. Guards: request attempt ceiling,
  stored metadata validation, retry cap, SQL ownership fence, expired-lease
  acknowledgement fence and abandoned-attempt ceiling.
- `GOWORK=off go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate`
  from stores/postgres: generated output unchanged.
- Root `GOWORK=off go test ./...`: passed.
- Adapter `GOWORK=off go test -race -count=1 ./...`, `go vet ./...`,
  `go build ./...` and `go mod verify`: passed against the published root
  v0.1.1, without a workspace replacement or checksum environment exception.
- Explicitly applied both example migrations, then ran
  `go run ./stores/postgres/examples/transactional`: exited successfully;
  SQL inspection found `state=succeeded`, `attempts=1`, empty diagnostic.
- `git diff --check`: passed. All source/artifact files are below 1,000 lines.

The first independent adapter dependency resolution encountered the checksum
service's cached pre-creation repository-not-found response for core v0.1.0.
Bootstrap tidy used temporary command-scoped GOPROXY=direct and a GONOSUMDB
exception for only that module. Core v0.1.1 aliases the identical source commit
and avoids the cached lookup. A completely fresh module cache downloaded and
checksum-verified v0.1.1 successfully with default Go network/checksum settings.
The adapter pins v0.1.1. No persistent Go settings or go.mod replacements were
introduced. Final adapter checks use the default checksum configuration.

Integration coverage includes same-transaction rollback/commit and pre-commit
invisibility; 16 concurrent claimers on one row; actual lease expiry/reclaim;
stale and expired ack/retry/fail; delay/backoff persistence; finite retries and
abandoned claims; invalid stored payload rejection and fixed diagnostics.
Core tests cover handler success, errors, panics, permanent/unregistered/invalid
work, timeout, shutdown, concurrency, registration snapshots and completed
outcomes racing shutdown.

GitHub Actions runs asynchronously on publication; local results do not claim
that remote CI has finished. Failed jobs and successful jobs are retained until
explicit operator cleanup. Handler cancellation remains cooperative and delivery
remains at least once.
