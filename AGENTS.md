# Working on Pluto Jobs

- Only edit this repository. Sibling Pluto projects are read-only references.
- Keep the core standard-library-only; pgx belongs in stores/postgres.
- Use explicit construction and registration. No startup DDL, tenant policy,
  automatic discovery, SMTP/webhook clients, brokers or scheduling features.
- Preserve at-least-once semantics, per-claim fencing and finite attempts,
  including abandoned claims. Never hold a transaction across a handler call.
- Never log payloads, raw errors or panic values. Persist only bounded codes.
- Callers own schema migration, commit/rollback, retention and cleanup.
- Keep every file below 1,000 lines; use the configured 80-column formatter
  and expanded whitespace. Do not compress code to satisfy file limits.
- Run scripts/fmt.sh, scripts/check.sh, scripts/test.sh -race,
  scripts/fuzz.sh and scripts/mutation-smoke.py. Real SQL tests require a
  disposable PLUTO_JOBS_TEST_DATABASE_URL; skipped tests are not passes.
- Keep optional modules independently publishable with real release pins.
  Local go.work replacements are allowed; go.mod replacements are not.
