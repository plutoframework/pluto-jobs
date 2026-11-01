package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	jobs "github.com/plutoframework/pluto-jobs"

	"github.com/jackc/pgx/v5/pgxpool"
)

func database(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("PLUTO_JOBS_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("PLUTO_JOBS_REQUIRE_DATABASE") == "1" {
			t.Fatal(
				"required Jobs SQL integration needs PLUTO_JOBS_TEST_DATABASE_URL",
			)
		}

		if os.Getenv("CI") != "" {
			t.Fatal("CI requires PLUTO_JOBS_TEST_DATABASE_URL")
		}

		t.Skip(
			"set PLUTO_JOBS_TEST_DATABASE_URL to a disposable Postgres database",
		)
	}

	ctx := context.Background()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}

	schema := "jobs_" + jobs.NewID()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	t.Cleanup(
		func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); admin.Close() },
	)

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}

	config.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	ddl, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}

	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}

	return store, pool
}

func newRequest(t *testing.T, attempts int) jobs.Request {
	t.Helper()

	tenant := "explicit-tenant"

	r, err := jobs.NewRequest(
		"email.welcome",
		struct{ OrderID string }{"synthetic"},
		jobs.Options{
			TenantID:    &tenant,
			MaxAttempts: attempts,
			AvailableAt: time.Now().Add(-time.Second),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func claim(t *testing.T, s *Store) jobs.Claim {
	t.Helper()

	c, err := s.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func expire(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()

	if _, err := pool.Exec(
		context.Background(),
		"UPDATE pluto_jobs SET lease_until = clock_timestamp() - interval '1 second' WHERE id=$1",
		id,
	); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionAtomicity(t *testing.T) {
	s, pool := database(t)

	ctx := context.Background()
	if _, err := pool.Exec(
		ctx,
		"CREATE TABLE orders (id text PRIMARY KEY)",
	); err != nil {
		t.Fatal(err)
	}

	for _, commit := range []bool{false, true} {
		r := newRequest(t, 3)

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = tx.Rollback(ctx) }()

		if _, err = tx.Exec(
			ctx,
			"INSERT INTO orders VALUES ($1)",
			r.ID,
		); err != nil {
			t.Fatal(err)
		}

		if err = s.EnqueueTx(ctx, tx, r); err != nil {
			t.Fatal(err)
		}

		if _, err = s.Claim(ctx, time.Minute); !errors.Is(err, jobs.ErrNoJob) {
			t.Fatalf("uncommitted job visible: %v", err)
		}

		if commit {
			err = tx.Commit(ctx)
		} else {
			err = tx.Rollback(ctx)
		}

		if err != nil {
			t.Fatal(err)
		}

		var orders, queued int
		if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM orders WHERE id=$1), (SELECT count(*) FROM pluto_jobs WHERE id=$1)", r.ID).
			Scan(&orders, &queued); err != nil {
			t.Fatal(err)
		}

		want := 0
		if commit {
			want = 1
		}

		if orders != want || queued != want {
			t.Fatalf("commit=%v: orders=%d jobs=%d", commit, orders, queued)
		}

		if commit {
			c := claim(t, s)
			if c.Job.ID != r.ID || c.Job.TenantID == nil ||
				*c.Job.TenantID != "explicit-tenant" ||
				c.Job.Validate() != nil {
				t.Fatal("stored metadata mismatch")
			}
		}
	}
}

func TestConcurrentClaimsExcludeOtherWorkers(t *testing.T) {
	s, _ := database(t)
	ctx := context.Background()

	r := newRequest(t, 3)
	if err := s.Enqueue(ctx, r); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	results := make(chan jobs.Claim, 16)
	failures := make(chan error, 16)

	for range 16 {
		wg.Go(func() {
			c, err := s.Claim(ctx, time.Minute)
			if err == nil {
				results <- c
			} else if !errors.Is(err, jobs.ErrNoJob) {
				failures <- err
			}
		})
	}

	wg.Wait()
	close(failures)

	for err := range failures {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("claims = %d", len(results))
	}

	c := <-results
	if err := s.Ack(ctx, c); err != nil {
		t.Fatal(err)
	}

	if err := s.Ack(ctx, c); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal("double ack accepted")
	}
}

func TestLeaseExpiryFencesStaleOwners(t *testing.T) {
	s, pool := database(t)
	ctx := context.Background()

	r := newRequest(t, 3)
	if err := s.Enqueue(ctx, r); err != nil {
		t.Fatal(err)
	}

	old, err := s.Claim(ctx, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(30 * time.Millisecond)

	if err = s.Ack(ctx, old); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("expired ack: %v", err)
	}

	if err = s.Retry(
		ctx,
		old,
		0,
		jobs.CodeHandler,
	); !errors.Is(
		err,
		jobs.ErrLeaseLost,
	) {
		t.Fatalf("expired retry: %v", err)
	}

	current := claim(t, s)
	if current.Token == old.Token || current.Job.Attempt != 2 {
		t.Fatal("claim identity/attempt not advanced")
	}

	for name, settle := range map[string]func() error{
		"ack":   func() error { return s.Ack(ctx, old) },
		"retry": func() error { return s.Retry(ctx, old, 0, jobs.CodeHandler) },
		"fail":  func() error { return s.Fail(ctx, old, jobs.CodePermanent) },
	} {
		if err = settle(); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("stale %s accepted: %v", name, err)
		}
	}

	// A stale owner must not alter the current lease or attempt.
	var (
		token    string
		attempts int
	)
	if err = pool.QueryRow(ctx, "SELECT claim_token,attempts FROM pluto_jobs WHERE id=$1", r.ID).
		Scan(&token, &attempts); err != nil {
		t.Fatal(err)
	}

	if token != current.Token || attempts != 2 {
		t.Fatal("stale worker changed row")
	}

	if err = s.Ack(ctx, current); err != nil {
		t.Fatal(err)
	}
}

func TestDelayRetryAndCrashExhaustion(t *testing.T) {
	s, pool := database(t)
	ctx := context.Background()
	r := newRequest(t, 2)

	r.AvailableAt = time.Now().Add(time.Hour)
	if err := s.Enqueue(ctx, r); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Claim(ctx, time.Minute); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal("early claim")
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE pluto_jobs SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1",
		r.ID,
	); err != nil {
		t.Fatal(err)
	}

	c := claim(t, s)
	if err := s.Retry(ctx, c, time.Hour, jobs.CodeHandler); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Claim(ctx, time.Minute); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal("ignored retry backoff")
	}

	var delayed bool
	if err := pool.QueryRow(ctx, "SELECT available_at > clock_timestamp() + interval '59 minutes' FROM pluto_jobs WHERE id=$1", r.ID).
		Scan(&delayed); err != nil ||
		!delayed {
		t.Fatal("retry delay not persisted", err)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE pluto_jobs SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1",
		r.ID,
	); err != nil {
		t.Fatal(err)
	}

	c = claim(t, s)
	if c.Job.Attempt != 2 {
		t.Fatal("attempt not consumed")
	}

	if err := s.Retry(ctx, c, 0, jobs.CodeHandler); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Claim(ctx, time.Minute); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal("exhausted retry requeued")
	}

	// Two abandoned claims consume both attempts, even without Retry or Ack.
	crash := newRequest(t, 2)
	if err := s.Enqueue(ctx, crash); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		abandoned := claim(t, s)
		if abandoned.Job.Attempt != attempt {
			t.Fatal("abandoned attempt not counted")
		}

		expire(t, pool, crash.ID)
	}

	if _, err := s.Claim(ctx, time.Minute); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal("exhausted crash requeued")
	}

	var (
		state, diagnostic string
		attempts          int
	)
	if err := pool.QueryRow(ctx, "SELECT state,diagnostic,attempts FROM pluto_jobs WHERE id=$1", crash.ID).
		Scan(&state, &diagnostic, &attempts); err != nil {
		t.Fatal(err)
	}

	if state != "failed" || diagnostic != "lease_expired" || attempts != 2 {
		t.Fatalf("%s %s %d", state, diagnostic, attempts)
	}
}

func TestPersistenceGuardsAndInvalidStoredWork(t *testing.T) {
	s, pool := database(t)
	ctx := context.Background()
	r := newRequest(t, 1)
	bad := r

	bad.MaxAttempts = 0
	if err := s.Enqueue(ctx, bad); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatal("invalid enqueue accepted")
	}

	if err := s.EnqueueTx(ctx, nil, r); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatal("nil transaction accepted")
	}

	if err := s.Enqueue(ctx, r); err != nil {
		t.Fatal(err)
	}

	if err := s.Enqueue(ctx, r); err == nil {
		t.Fatal("duplicate ID accepted")
	}

	c := claim(t, s)
	if err := s.Retry(
		ctx,
		c,
		-1,
		jobs.CodeHandler,
	); !errors.Is(
		err,
		jobs.ErrInvalid,
	) {
		t.Fatal("negative delay")
	}

	if err := s.Fail(
		ctx,
		c,
		jobs.Code("secret"),
	); !errors.Is(
		err,
		jobs.ErrInvalid,
	) {
		t.Fatal("unbounded diagnostic")
	}

	if err := s.Fail(ctx, c, jobs.CodePermanent); err != nil {
		t.Fatal(err)
	}

	// A row tampered with outside the trusted enqueue API fails before dispatch.
	r = newRequest(t, 1)
	if err := s.Enqueue(ctx, r); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE pluto_jobs SET payload=$2 WHERE id=$1",
		r.ID,
		[]byte("bad-json"),
	); err != nil {
		t.Fatal(err)
	}

	config := jobs.DefaultConfig()
	config.Concurrency = 1
	config.PollInterval = time.Millisecond
	called := false

	w, err := jobs.NewWorker(
		s,
		map[jobs.Kind]jobs.Handler{
			r.Kind: func(context.Context, jobs.Job) error { called = true; return nil },
		},
		config,
	)
	if err != nil {
		t.Fatal(err)
	}

	workerCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	if err = w.Run(workerCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}

	var diagnostic string
	if err = pool.QueryRow(ctx, "SELECT diagnostic FROM pluto_jobs WHERE id=$1", r.ID).
		Scan(&diagnostic); err != nil {
		t.Fatal(err)
	}

	if called || diagnostic != "invalid" {
		t.Fatal("invalid stored work reached handler")
	}
}
