# Pluto Jobs

A small Go transactional outbox and durable background worker. Applications
supply handlers for email, webhooks, external audit or other work. The library
provides persistence and delivery accounting, not network clients.

Go 1.25.5 or newer. MIT licensed. Initial release; not independently audited.

## Install

```sh
go get github.com/plutoframework/pluto-jobs@v0.1.1
go get github.com/plutoframework/pluto-jobs/stores/postgres@v0.1.0
```

The root module uses only the standard library. The optional Postgres module
pins pgx/v5 v5.11.0 and depends on the published core release. sqlc v1.30.0 and
golangci-lint v2.15.0 are tooling, not runtime dependencies.

## Enqueue an order and deliver it with a worker

This complete application adapter inserts an order and enqueues `order.created`
in one PostgreSQL transaction. Its worker decodes the typed payload and calls an
injected real sender. Keep business authorization before `Insert`; the tenant
comes from verified server context, not an arbitrary request header.

Apply the library's `stores/postgres/schema.sql` and this application table using
your migration system before startup:

```sql
CREATE TABLE orders (
    tenant_id text NOT NULL,
    id text NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
```

The complete source is in `stores/postgres/examples/orders/orders.go`:

<!-- example-source: stores/postgres/examples/orders/orders.go -->

```go
// Package orders shows atomic enqueue and a worker with injected delivery.
package orders

import (
	"context"
	"errors"
	"time"

	jobs "github.com/plutoframework/pluto-jobs"
	"github.com/plutoframework/pluto-jobs/stores/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

const Created jobs.Kind = "order.created"

type OrderCreated struct {
	OrderID string `json:"order_id"`
}

// Sender implements the application's real, context-aware delivery adapter.
// It must tolerate repeated delivery with the same job ID and tenant.
type Sender interface {
	Send(context.Context, string, string, OrderCreated) error
}

// Insert follows business authorization. Tenant is resolved by the server.
// The caller applies the orders and outbox schemas before invoking this method.
func Insert(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenant, orderID string,
) (string, error) {
	if pool == nil || tenant == "" || orderID == "" {
		return "", errors.New("order requires a pool, tenant and ID")
	}

	store, err := postgres.New(pool)
	if err != nil {
		return "", err
	}

	request, err := jobs.NewRequest(
		Created,
		OrderCreated{OrderID: orderID},
		jobs.Options{TenantID: &tenant, MaxAttempts: 5},
	)
	if err != nil {
		return "", err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}

	defer func() {
		cleanup, cancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		defer cancel()

		_ = tx.Rollback(cleanup)
	}()

	_, err = tx.Exec(
		ctx,
		"INSERT INTO orders (tenant_id, id) VALUES ($1, $2)",
		tenant,
		orderID,
	)
	if err != nil {
		return "", err
	}

	if err := store.EnqueueTx(ctx, tx, request); err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {

		// Keep this ID to reconcile an uncertain commit; do not enqueue anew.
		return request.ID, err
	}

	return request.ID, nil
}

func Delivery(sender Sender) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		payload, err := jobs.Decode[OrderCreated](job)
		if err != nil {
			return jobs.Permanent(err)
		}

		if payload.OrderID == "" || job.TenantID == nil || sender == nil {
			return jobs.Permanent(errors.New("incomplete order delivery"))
		}

		return sender.Send(ctx, job.ID, *job.TenantID, payload)
	}
}

func Worker(store jobs.Store, sender Sender) (*jobs.Worker, error) {
	if sender == nil {
		return nil, errors.New("delivery sender is required")
	}

	return jobs.NewWorker(
		store,
		map[jobs.Kind]jobs.Handler{Created: Delivery(sender)},
		jobs.DefaultConfig(),
	)
}
```

<!-- /example-source -->

`Insert` returns the stable job ID after commit. It also returns that ID when
commit reports an uncertain outcome: retain it and reconcile before submitting
another request. Rollback removes both the order and its job. `Enqueue` outside
this transaction cannot make an independent business write atomic.

### Construct and run the worker

This application-startup excerpt assumes an existing `pool`, cancellation-aware
`ctx` and your concrete implementation of the example's `Sender` interface:

```go
store, err := postgres.New(pool)
if err != nil {
	return err
}

worker, err := orders.Worker(store, sender)
if err != nil {
	return err
}

return worker.Run(ctx)
```

Import the local example package under your own application's module path.
`Sender.Send` performs actual delivery and returns its error. Use the job ID and
tenant as the destination's idempotency identity across repeated attempts; do
not acknowledge success merely because a message was printed or scheduled.
Context cancellation does not undo a delivery already accepted downstream.

For a complete concrete destination, the
[purchasing app worker](https://github.com/plutoframework/pluto-example/blob/main/cmd/worker/main.go)
archives audit events in PostgreSQL through a tenant/event uniqueness boundary.
That demonstrates durable local archival, not an external email or SIEM service.

### Observe failure and retry behavior

The order example's tests verify that the stable job ID and tenant reach the
sender, delivery errors propagate for worker accounting, and invalid metadata
fails before any send. Run from the Postgres module:

```sh
cd stores/postgres
go test -race ./examples/orders
```

A sender error retries under the worker's finite policy. `jobs.Permanent(err)`
marks unrecoverable payload/domain failures. Applications must classify their
provider-specific errors; the library cannot infer whether delivery happened
from a timeout. Delivery remains at least once.

## Request and worker contracts

`NewRequest[T]` marshals typed JSON, validates metadata and creates the stable
job ID. Payloads are bounded to 65,536 bytes; kinds and attempt counts are
validated. Tenant metadata is explicit and supplies no authorization. Only
trusted application code should enqueue jobs or access their persistence tables.

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

## Testing: transactional outbox with a synthetic handler

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
