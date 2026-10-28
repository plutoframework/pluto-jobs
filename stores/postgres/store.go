// Package postgres implements the jobs store using caller-managed Postgres.
package postgres

import (
	"context"
	"errors"
	"time"

	jobs "github.com/plutoframework/pluto-jobs"
	"github.com/plutoframework/pluto-jobs/stores/postgres/internal/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store never creates schema or closes its caller-owned pool.
type Store struct{ queries *db.Queries }

func New(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, jobs.ErrInvalid
	}

	return &Store{queries: db.New(pool)}, nil
}

func (s *Store) Enqueue(ctx context.Context, request jobs.Request) error {
	return enqueue(ctx, s.queries, request)
}

// EnqueueTx uses exactly the supplied business transaction. The caller alone
// owns commit/rollback. Never use Enqueue for transactional business changes.
func (s *Store) EnqueueTx(
	ctx context.Context,
	tx pgx.Tx,
	request jobs.Request,
) error {
	if tx == nil {
		return jobs.ErrInvalid
	}

	return enqueue(ctx, db.New(tx), request)
}

func enqueue(ctx context.Context, q *db.Queries, r jobs.Request) error {
	if err := r.Validate(); err != nil {
		return err
	}

	tenant := pgtype.Text{}
	if r.TenantID != nil {
		tenant = pgtype.Text{String: *r.TenantID, Valid: true}
	}

	return q.Enqueue(
		ctx,
		db.EnqueueParams{
			ID:          r.ID,
			Kind:        string(r.Kind),
			Payload:     r.Payload,
			TenantID:    tenant,
			AvailableAt: pgtype.Timestamptz{Time: r.AvailableAt, Valid: true},
			MaxAttempts: int32(r.MaxAttempts),
		},
	)
}

// Claim acquires at most one row, committing before returning. Expired final
// attempts are marked failed; that maintenance step returns ErrNoJob. The next
// poll continues with other rows, keeping maintenance work bounded.
func (s *Store) Claim(
	ctx context.Context,
	lease time.Duration,
) (jobs.Claim, error) {
	if lease < time.Millisecond || lease > 24*time.Hour {
		return jobs.Claim{}, jobs.ErrInvalid
	}

	row, err := s.queries.Claim(
		ctx,
		db.ClaimParams{Token: jobs.NewID(), LeaseUs: lease.Microseconds()},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Claim{}, jobs.ErrNoJob
	}

	if err != nil {
		return jobs.Claim{}, err
	}

	if row.State != "running" {
		return jobs.Claim{}, jobs.ErrNoJob
	}

	request := jobs.Request{
		ID:          row.ID,
		Kind:        jobs.Kind(row.Kind),
		Payload:     row.Payload,
		AvailableAt: row.AvailableAt.Time,
		MaxAttempts: int(row.MaxAttempts),
	}
	if row.TenantID.Valid {
		tenant := row.TenantID.String
		request.TenantID = &tenant
	}

	return jobs.Claim{
		Job:   jobs.Job{Request: request, Attempt: int(row.Attempts)},
		Token: row.ClaimToken.String,
	}, nil
}

func affected(count int64, err error) error {
	if err != nil {
		return err
	}

	if count != 1 {
		return jobs.ErrLeaseLost
	}

	return nil
}

func (s *Store) Ack(ctx context.Context, claim jobs.Claim) error {
	return affected(
		s.queries.Ack(
			ctx,
			db.AckParams{
				ID:         claim.Job.ID,
				ClaimToken: pgtype.Text{String: claim.Token, Valid: true},
			},
		),
	)
}

func (s *Store) Retry(
	ctx context.Context,
	claim jobs.Claim,
	delay time.Duration,
	code jobs.Code,
) error {
	if delay < 0 || delay > 24*time.Hour || !code.Valid() {
		return jobs.ErrInvalid
	}

	return affected(
		s.queries.Retry(
			ctx,
			db.RetryParams{
				ID:         claim.Job.ID,
				Token:      pgtype.Text{String: claim.Token, Valid: true},
				DelayUs:    delay.Microseconds(),
				Diagnostic: string(code),
			},
		),
	)
}

func (s *Store) Fail(
	ctx context.Context,
	claim jobs.Claim,
	code jobs.Code,
) error {
	if !code.Valid() {
		return jobs.ErrInvalid
	}

	return affected(
		s.queries.Fail(
			ctx,
			db.FailParams{
				ID:         claim.Job.ID,
				ClaimToken: pgtype.Text{String: claim.Token, Valid: true},
				Diagnostic: string(code),
			},
		),
	)
}

var _ jobs.Store = (*Store)(nil)
