-- Caller-owned migration. Apply once through your migration system, never startup.
CREATE TABLE pluto_jobs (
    id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    kind text NOT NULL CHECK (length(kind) BETWEEN 1 AND 128),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 65536),
    tenant_id text CHECK (octet_length(tenant_id) BETWEEN 1 AND 256),
    available_at timestamptz NOT NULL,
    max_attempts integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 100),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 100),
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed')),
    claim_token text,
    lease_until timestamptz,
    diagnostic text NOT NULL DEFAULT '' CHECK (diagnostic IN (
        '', 'handler_error', 'handler_panic', 'permanent', 'unregistered',
        'invalid', 'timeout', 'canceled', 'lease_expired'
    )),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    CHECK ((state = 'running') =
        (claim_token IS NOT NULL AND lease_until IS NOT NULL))
);

CREATE INDEX pluto_jobs_pending ON pluto_jobs (available_at, id)
    WHERE state = 'pending';
CREATE INDEX pluto_jobs_expired ON pluto_jobs (lease_until, id)
    WHERE state = 'running';
