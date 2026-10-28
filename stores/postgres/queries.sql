-- name: Enqueue :exec
INSERT INTO pluto_jobs (id, kind, payload, tenant_id, available_at, max_attempts)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: Claim :one
WITH picked AS (
    SELECT id FROM pluto_jobs
    WHERE (state = 'pending' AND available_at <= clock_timestamp())
       OR (state = 'running' AND lease_until <= clock_timestamp())
    ORDER BY available_at, id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE pluto_jobs AS j SET
    state = CASE WHEN j.attempts >= j.max_attempts THEN 'failed'
        ELSE 'running' END,
    attempts = CASE WHEN j.attempts >= j.max_attempts THEN j.attempts
        ELSE j.attempts + 1 END,
    claim_token = CASE WHEN j.attempts >= j.max_attempts THEN NULL
        ELSE sqlc.arg(token)::text END,
    lease_until = CASE WHEN j.attempts >= j.max_attempts THEN NULL
        ELSE clock_timestamp() + sqlc.arg(lease_us)::bigint *
            interval '1 microsecond' END,
    diagnostic = CASE WHEN j.state = 'running' THEN 'lease_expired'
        ELSE j.diagnostic END,
    finished_at = CASE WHEN j.attempts >= j.max_attempts
        THEN clock_timestamp() ELSE NULL END
FROM picked WHERE j.id = picked.id
RETURNING j.*;

-- name: Ack :execrows
UPDATE pluto_jobs SET state = 'succeeded', claim_token = NULL,
    lease_until = NULL, finished_at = clock_timestamp(), diagnostic = ''
WHERE id = $1 AND claim_token = $2 AND state = 'running'
    AND lease_until > clock_timestamp();

-- name: Retry :execrows
UPDATE pluto_jobs SET
    state = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
    available_at = clock_timestamp() + sqlc.arg(delay_us)::bigint *
        interval '1 microsecond',
    finished_at = CASE WHEN attempts >= max_attempts THEN clock_timestamp()
        ELSE NULL END,
    diagnostic = sqlc.arg(diagnostic), claim_token = NULL, lease_until = NULL
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(token) AND state = 'running'
    AND lease_until > clock_timestamp();

-- name: Fail :execrows
UPDATE pluto_jobs SET state = 'failed', claim_token = NULL,
    lease_until = NULL, diagnostic = $3, finished_at = clock_timestamp()
WHERE id = $1 AND claim_token = $2 AND state = 'running'
    AND lease_until > clock_timestamp();
