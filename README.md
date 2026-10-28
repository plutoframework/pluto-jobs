# Pluto Jobs

A small Go transactional outbox and durable background worker. Applications
supply handlers for email, webhooks, external audit or other work. The library
provides persistence and delivery accounting, not network clients.

Go 1.25.5 or newer. MIT licensed. Initial release; not independently audited.

## Install

```sh
go get github.com/plutoframework/pluto-jobs@v0.1.0
go get github.com/plutoframework/pluto-jobs/stores/postgres@v0.1.0
```

The root module uses only the standard library. The optional Postgres module
pins pgx/v5 v5.11.0 and depends on the published core release. sqlc v1.30.0 and
golangci-lint v2.15.0 are tooling, not runtime dependencies.

## Transactional enqueue

Apply `stores/postgres/schema.sql` through your own migration system first.
`postgres.New(pool)` performs no DDL and never closes the caller-owned pool.

```go
store, err := postgres.New(pool)
if err != nil { return err }
tenant := "explicit-tenant"
request, err := jobs.NewRequest("order.created", OrderCreated{OrderID: order.ID},
    jobs.Options{TenantID: &tenant, MaxAttempts: 5})
if err != nil { return err }
tx, err := pool.Begin(ctx)
if err != nil { return err }
defer tx.Rollback(ctx)
if _, err = tx.Exec(ctx, "INSERT INTO orders (id) VALUES ($1)", order.ID); err != nil {
    return err
}
if err = store.EnqueueTx(ctx, tx, request); err != nil { return err }
return tx.Commit(ctx)
```

The business write and enqueue use **the same pgx transaction**. Rollback
removes both; commit exposes both. The caller owns commit/rollback. Calling
`store.Enqueue` instead is an independent insert and cannot provide atomicity
with a separate business transaction. Retain the generated request ID if a
commit outcome is uncertain; duplicate IDs are rejected, not silently accepted.
Determine the transaction's outcome before deciding to enqueue another ID.

`NewRequest[T]` marshals typed data. `Enqueue[T]` combines construction with an
independent insert for work that does not accompany a business transaction.
Payloads must be valid JSON, 1–65,536 bytes. Kinds use 1–128 ASCII letters,
digits, dots, underscores or hyphens. Max attempts default to 5 and must be
1–100. An explicit `AvailableAt` delays work; the default is the enqueue-side
clock's current time. Due/lease decisions use Postgres time, so synchronize
application/database clocks. Tenant metadata is optional, explicit, nonblank
and at most 256 bytes. No tenant is inferred; jobs do not perform tenant auth
or RBAC. Only trusted application code should enqueue or access these tables.

## Worker

```go
worker, err := jobs.NewWorker(store, map[jobs.Kind]jobs.Handler{
    "order.created": func(ctx context.Context, job jobs.Job) error {
        payload, err := jobs.Decode[OrderCreated](job)
        if err != nil { return jobs.Permanent(err) }
        return sender.Send(ctx, job.ID, job.TenantID, payload)
    },
}, jobs.DefaultConfig())
if err != nil { return err }
return worker.Run(ctx)
```

Registrations are copied at construction. Stored metadata and JSON are
validated before dispatch; `Decode[T]` also checks the typed shape. Handlers
must be concurrency-safe and validate their own domain data. A missing handler,
invalid stored metadata or `Permanent(err)` fails immediately. Other errors,
recovered panics and cooperative timeouts retry until attempts are exhausted.
Terminal state is `failed`; there is no second dead-letter queue. A successful
handler returning nil is acknowledged even if shutdown was requested meanwhile.

Defaults: 4 handlers in parallel, 1-second empty polling, 60-second leases,
30-second handler deadlines, 5-second store deadlines, exponential retry from
1 second capped at 1 hour. Retry delay is `min(base * 2^(attempt-1), max)`.
Configuration is explicit: concurrency 1–256; positive polling/deadlines/base;
lease at most 24 hours, store timeout at most 1 hour, retry cap at most 24 hours.
The lease must exceed the handler timeout plus twice the store timeout, leaving
time for claim transport and settlement. Each worker claims one job at a time
per slot, without prefetching. Empty polls and exhausted-lease cleanup each do
bounded work. Run returns a sanitized error on a store failure; the application
owns supervision/restart. Cancellation returns the context error.

There is no heartbeat or renewal API in this MVP. Set a finite handler timeout
appropriate to the operation. Its context is canceled before the configured
lease can expire under normal timing. Postgres can reclaim an expired claim;
ack/retry/fail require its unique claim token **and an unexpired lease**. A stale
worker cannot settle a newer claim. Database transactions end before handler
execution. Abandoned claims consume attempts; expiry after the final claim is
marked failed by a later worker poll, even if no handler ever returned.

`Run` cancels handlers on shutdown and waits for them, then gives settlement a
fresh bounded context. Cancellation is cooperative: Go cannot kill a handler
that ignores its context, stop its external side effects, or guarantee a shutdown
deadline for it. Such a handler can outlive its lease and overlap a replacement;
late settlement is fenced. No detached handler goroutines hide that limitation.
Configure external request timeouts and have your process supervisor enforce
an eventual exit if needed. Lease durations assume a reasonably stable DB clock.

## Delivery contract and operations

Delivery is **at least once**, never exactly once. An external side effect may
succeed immediately before a worker crashes or loses its acknowledgement.
Use stable `job.ID` as a provider idempotency key, or make the handler's business
operation idempotent. A finite attempt budget means eventual success is not
promised. Deploy compatible handlers before producers and retain them until
old work drains; unregistered kinds fail rather than waiting for a new deploy.

The library logs nothing. Persisted diagnostics are a fixed vocabulary, never
raw handler/provider errors, panic values or stack traces. Payloads and tenant
metadata remain in the database, including failed and succeeded rows. Direct
adapter errors are returned to callers and may contain database details; do not
blindly log them. Worker errors are sanitized. Applications should add their
own sanitized monitoring and protect/encrypt sensitive application data.

The caller owns retention, inspection, backups and explicit cleanup. There is
no automatic deletion, replay API or failed-job UI. Inspect `state`, `attempts`,
`diagnostic`, `finished_at` and `lease_until` with your administrative tooling.
For example, an operator can delete terminal rows older than its retention
policy in bounded batches; never delete active claims. Review payloads and
idempotency implications before explicitly constructing replacement work.

Out of scope: cron/schedulers, DAGs, bulk management, deduplication frameworks,
other databases, brokers, native senders, provider integrations and benchmarks.

## Runnable synthetic example

Use a disposable database; these commands apply explicit caller migrations.
No external messages are sent. With `DATABASE_URL` exported:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f stores/postgres/schema.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 \
  -f stores/postgres/examples/transactional/schema.sql
cd stores/postgres
go run ./examples/transactional
```

The example inserts a synthetic order and job in one transaction, runs a handler,
prints only its job ID/attempt, acknowledges it and exits. Use a fresh schema
for repeated migration runs. It is intentionally not a provider integration.

## Development and checks

```sh
mise trust
mise install
# Development workspace only; never add replacements to go.mod:
go work init . ./stores/postgres
./scripts/fmt.sh
./scripts/check.sh
./scripts/test.sh -race
./scripts/fuzz.sh
python3 scripts/mutation-smoke.py
```

For real integration tests, create a disposable Postgres instance and export
`PLUTO_JOBS_TEST_DATABASE_URL`. Tests create/drop uniquely named schemas; never
point them at an application database. Without the URL they explicitly skip;
CI treats a missing URL as a failure.

```sh
docker run -d --rm --name pluto-jobs-test \
  -e POSTGRES_PASSWORD=jobs -e POSTGRES_DB=jobs \
  -p 127.0.0.1:55432:5432 postgres:17-alpine
export PLUTO_JOBS_TEST_DATABASE_URL='postgres://postgres:jobs@127.0.0.1:55432/jobs?sslmode=disable'
./scripts/test.sh -race
python3 scripts/mutation-smoke.py
docker stop pluto-jobs-test
```

Mutation smoke independently changes validation, retry bounds, SQL lease and
ownership guards and abandoned-attempt accounting. It reports killed, survived,
build failure and timeout separately; SQL mutants require the database URL.
Fuzzing runs a bounded request-validation target. No performance claims are made.

## Code tour

- `job.go`: typed requests, IDs, payload/metadata validation and decoding.
- `worker.go`: handler snapshot, concurrency, deadlines and retry accounting.
- `stores/postgres/store.go`: transaction-aware enqueue and fenced settlement.
- `stores/postgres/schema.sql`: explicit caller-owned table/index migration.
- `stores/postgres/queries.sql`: claim/settle SQL; `internal/db` is sqlc output.
- `stores/postgres/integration_test.go`: real transactional/concurrent tests.
- `stores/postgres/examples/transactional`: compiling synthetic example.
- `scripts`, `mise.toml`, `.golangci.yml`: pinned checks and formatting.
