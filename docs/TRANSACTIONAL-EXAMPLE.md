# Commit an order and deliver its outbox job

This walkthrough runs the complete program in
[`stores/postgres/examples/transactional`](../stores/postgres/examples/transactional/main.go).
It uses a synthetic handler: no email, webhook or paid service is called.
Run commands from the repository root unless a step says otherwise.

## 1. Start a disposable database

Requires Docker and Go 1.25.5+. The container name and port must be unused:

```sh
docker run --rm -d --name pluto-jobs-readme \
  -e POSTGRES_USER=pluto -e POSTGRES_PASSWORD=local_only \
  -e POSTGRES_DB=pluto_readme \
  -p 127.0.0.1:55441:5432 postgres:17.6-alpine
```

Wait until `docker exec pluto-jobs-readme pg_isready -U pluto -d pluto_readme`
succeeds. These are disposable local credentials, never a production preset.

## 2. Apply both schemas explicitly

```sh
docker exec -i pluto-jobs-readme psql -v ON_ERROR_STOP=1 \
  -U pluto -d pluto_readme < stores/postgres/schema.sql
docker exec -i pluto-jobs-readme psql -v ON_ERROR_STOP=1 \
  -U pluto -d pluto_readme < stores/postgres/examples/transactional/schema.sql
```

Library constructors perform no DDL. The application owns migration ordering.

## 3. Commit and process

```sh
cd stores/postgres
DATABASE_URL='postgres://pluto:local_only@127.0.0.1:55441/pluto_readme?sslmode=disable' \
  go run ./examples/transactional
cd ../..
```

The program constructs a typed request, begins one pgx transaction, inserts the
order, calls `EnqueueTx`, commits, and runs an explicitly registered handler. The
handler validates the payload and reports synthetic completion. Settlement uses a
bounded context, so cancellation after the handler does not lose its ack.

## 4. Check durable state

```sh
docker exec pluto-jobs-readme psql -U pluto -d pluto_readme -c \
  "SELECT state, attempts FROM pluto_jobs;"
docker exec pluto-jobs-readme psql -U pluto -d pluto_readme -c \
  "SELECT count(*) FROM pluto_jobs_demo_orders;"
```

After one run, expect one order and one job in `succeeded` with `attempts=1`.
Worker delivery remains at least once; real handlers need idempotency and
cooperative cancellation. Business rollback/commit and lease-fencing regressions
live in `stores/postgres/integration_test.go` and require a disposable test DB.

## 5. Clean up

```sh
docker stop pluto-jobs-readme
```
