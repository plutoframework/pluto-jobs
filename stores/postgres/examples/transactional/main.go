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
