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
