<p align="center">
  <img src="https://raw.githubusercontent.com/plutoframework/pluto/main/assets/readme/banner.svg" alt="Pluto — a small framework, wide open possibilities" width="100%" />
</p>

# Pluto Jobs

Commit the work. Deliver the side effect.

[Website](https://plutoframework.github.io/pluto-site/) · [Documentation](https://plutoframework.github.io/pluto-site/docs/backend/jobs/) · [CLI demo](https://github.com/plutoframework/pluto/blob/main/docs/DEMO.md) · [First issues](https://github.com/plutoframework/pluto/blob/main/CONTRIBUTING.md#starter-tasks)

## What you get

- Typed jobs and explicit handlers for application-owned delivery.
- Same-transaction PostgreSQL outbox enqueue with finite retries.
- Expiring leases and fenced completion for at-least-once workers.

## Get started

### 1. Install

From a Go **1.25.5+** module:

```sh
go get github.com/plutoframework/pluto-jobs@v0.1.1
go get github.com/plutoframework/pluto-jobs/stores/postgres@v0.1.0
```

The core is standard-library-only; the PostgreSQL adapter is a separate module.

### 2. Commit the business write and outbox request together

The complete synthetic program below inserts an order and enqueues its job in one
transaction, then runs an explicitly registered worker. It uses PostgreSQL and
prints completion instead of contacting a delivery provider.

Follow the [copyable Docker/schema/run/verification steps](docs/TRANSACTIONAL-EXAMPLE.md)
first, then run the checked-in program from `stores/postgres` with
`DATABASE_URL` configured: `go run ./examples/transactional`.

<!-- example-source: stores/postgres/examples/transactional/main.go -->

```go
// A synthetic transactional outbox example; no email or webhook is sent.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	jobs "github.com/plutoframework/pluto-jobs"
	"github.com/plutoframework/pluto-jobs/stores/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

type orderCreated struct {
	OrderID string `json:"order_id"`
}

func run(ctx context.Context) error {
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	store, err := postgres.New(pool)
	if err != nil {
		return err
	}

	tenant := "synthetic-tenant"
	orderID := jobs.NewID()

	request, err := jobs.NewRequest(
		"order.created",
		orderCreated{OrderID: orderID},
		jobs.Options{TenantID: &tenant},
	)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err = tx.Exec(
		ctx,
		"INSERT INTO pluto_jobs_demo_orders (id) VALUES ($1)",
		orderID,
	); err != nil {
		return err
	}

	if err = store.EnqueueTx(ctx, tx, request); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return err
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	handler := func(_ context.Context, job jobs.Job) error {
		if _, decodeErr := jobs.Decode[orderCreated](job); decodeErr != nil {
			return jobs.Permanent(decodeErr)
		}

		// Supply your sender here. Use job.ID as the provider idempotency key.
		// TenantID is explicit stored metadata; the app owns tenant authorization.
		fmt.Printf(
			"synthetic delivery completed: job=%s attempt=%d\n",
			job.ID,
			job.Attempt,
		)
		cancel()

		return nil
	}
	config := jobs.DefaultConfig()
	config.Concurrency = 1

	worker, err := jobs.NewWorker(
		store,
		map[jobs.Kind]jobs.Handler{"order.created": handler},
		config,
	)
	if err != nil {
		return err
	}

	err = worker.Run(workerCtx)
	if errors.Is(err, context.Canceled) {
		return nil
	}

	return err
}

func main() {
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	if err := run(ctx); err != nil {
		// Avoid printing raw provider/database errors that may contain secrets.
		fmt.Fprintln(os.Stderr, "transactional example failed")
		os.Exit(1)
	}
}
```

<!-- /example-source -->

### 3. Verify durable completion

After one run, the guide checks that one order exists and its job is `succeeded`
with `attempts=1`. The adapter [integration tests](stores/postgres/integration_test.go)
exercise rollback, commit visibility and lease fencing. Constructors never apply
schema. Real handlers should use the stable job ID as a provider idempotency key.

## Read further

- [API and integration reference](docs/REFERENCE.md)
- [Field guide](https://plutoframework.github.io/pluto-site/docs/backend/jobs/)
- [Pluto ecosystem](https://github.com/plutoframework/pluto#the-ecosystem)

## Boundaries

Delivery is at least once. Handlers must tolerate duplicates and cooperate with cancellation. Applications own migrations, tenant policy, retention and cleanup; no exactly-once guarantee or heartbeat renewal is implied.

## Contribute

Start with [CONTRIBUTING.md](CONTRIBUTING.md), then choose a [good first issue](https://github.com/plutoframework/pluto/blob/main/CONTRIBUTING.md#starter-tasks).

MIT licensed. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
