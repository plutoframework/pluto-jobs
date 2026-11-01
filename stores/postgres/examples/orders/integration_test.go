package orders

import (
	"context"
	"os"
	"testing"
	"time"

	jobs "github.com/plutoframework/pluto-jobs"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInsertCommitsOrderAndJobTogether(t *testing.T) {
	url := os.Getenv("PLUTO_JOBS_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("PLUTO_JOBS_REQUIRE_DATABASE") == "1" {
			t.Fatal(
				"required Jobs SQL integration needs PLUTO_JOBS_TEST_DATABASE_URL",
			)
		}

		t.Skip("requires disposable PLUTO_JOBS_TEST_DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	schema := "orders_example_" + jobs.NewID()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}

	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()

		_, _ = admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE")
	}()

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}

	config.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	outbox, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, string(outbox)); err != nil {
		t.Fatal(err)
	}

	orders, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, string(orders)); err != nil {
		t.Fatal(err)
	}

	id, err := Insert(ctx, pool, "north", "order-42")
	if err != nil || id == "" {
		t.Fatalf("order insert failed: %v", err)
	}

	var persisted string
	if err := pool.QueryRow(ctx,
		"SELECT id FROM pluto_jobs WHERE tenant_id = $1", "north",
	).Scan(&persisted); err != nil || persisted != id {
		t.Fatal("committed job identity missing")
	}

	// Reject the next outbox insert after its business INSERT has succeeded.
	_, err = pool.Exec(
		ctx,
		"ALTER TABLE pluto_jobs ADD CONSTRAINT reject_delivery CHECK (false) NOT VALID",
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Insert(ctx, pool, "north", "order-43"); err == nil {
		t.Fatal("outbox constraint failure was ignored")
	}

	var count int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM orders WHERE id = $1", "order-43",
	).Scan(&count); err != nil || count != 0 {
		t.Fatal("business write survived failed enqueue")
	}
}
