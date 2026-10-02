# Independent services

This example pins `v1.0.0-alpha.3` and uses its `MainPackage` support. Inside this repository, Go's workspace uses the matching checked-out framework sources. For an independent copy, run `GOWORK=off go mod tidy` first and use `GOWORK=off` for the commands below. It has one `go.mod`, shared domain apps, and three separate executables under `services/`; there is no `cmd/` or mode environment variable.

| Executable | Command | Dependencies |
| --- | --- | --- |
| `manage` | `migrate`, `seed` | PostgreSQL only |
| `api` | `serve` | PostgreSQL only |
| `reports` | `serve` | PostgreSQL only |
| `reports` | `report` | PostgreSQL, Redis and the API endpoint |
| `worker` | `worker` | Redis only; no PostgreSQL or catalog imports |

Both HTTP services expose `/products/count/`, `/health/live/` and `/health/ready/`. The count is deliberately public demonstration data. There are no HTTP write endpoints, Admin, sessions or signing keys. Add authentication and authorization before exposing private business data; sharing a database or private network does not authenticate callers.

## Native setup

From `examples/services`, copy `.env.example` to `.env` if it does not already exist. Supply URLs for your local PostgreSQL 16+ database and Redis 7.2+ instance; do not point this demo at an existing application database.

```sh
make build
./bin/manage migrate
./bin/manage seed
```

Run these in separate terminals, from the same project root:

```sh
./bin/api serve --addr=127.0.0.1:8000
./bin/reports serve --addr=127.0.0.1:8001
./bin/worker worker
```

Then:

```sh
curl http://127.0.0.1:8000/products/count/
curl http://127.0.0.1:8001/products/count/
./bin/reports report --api-url=http://127.0.0.1:8000
```

The two endpoints return `{"count":2}` on a fresh seeded database. `report` reads that database, calls the API, publishes a `reports.double_count` task, prints its receipt and waits at most 30 seconds for the worker's result:

```json
{"api_count":2,"database_count":2,"task_id":"<generated UUID>"}
{"doubled_count":4}
```

This is not an atomic snapshot across processes. Counts may differ under concurrent writes. The task is a pure calculation that tolerates redelivery; for database mutations coupled to publication, use the transactional outbox.

Only `report` requires Redis in the reports executable; its HTTP server does not open a queue connection. The worker needs neither `GOGO_DATABASE_URL` nor `GOGO_SECRET_KEY`. All declared supplied settings are still validated; unknown `GOGO_*` variables are errors.

## Docker

Set `POSTGRES_PASSWORD` in the example's `.env` to a random hex value (for example, generate one with `openssl rand -hex 32`). It is Docker's bootstrap input, not a new framework setting. Compose constructs `GOGO_DATABASE_URL` and supplies `GOGO_REDIS_URL`; native URL values from `.env` are not injected into containers.

```sh
docker compose up --build -d
docker compose exec reports /app/service report
docker compose logs worker
docker compose down
```

Open [the API count](http://localhost:8000/products/count/) and [the reports count](http://localhost:8001/products/count/). `migrate` and `seed` run once before the HTTP processes. Every service is built from its own entrypoint and runs as a non-root user without source or secrets in its image.

The local Compose stack shares a network namespace so the loopback-only development Redis contract is preserved. PostgreSQL and Redis are not published to the host. This is a local demonstration, not a production topology: deploy each binary independently in production with TLS, scoped credentials, networking and per-service health checks. Ordinary `down` preserves volumes; deleting volumes destroys the demonstration data.

## Extend a service

```sh
go run manage.go startservice sessions
go run ./services/sessions runserver --reload --addr=127.0.0.1:8002
go build -o bin/sessions ./services/sessions
```

Use the package path, not `services/sessions/main.go` alone: the project factory and routes live in sibling Go files. Shared models/migrations stay in `apps/`. Large services can add `services/<name>/internal/` without another module. Shared configuration definitions live in `config/settings` so importing them does not pull the management project's database imports into workers.

## Verify

```sh
make test
go run manage.go generate --check
go run manage.go makemigrations catalog --check
```

From the framework root, the isolated native integration test builds all four binaries, applies migrations, seeds twice, runs two HTTP processes plus a worker, verifies the API/database/task result and checks that stopping the API does not stop reports:

```sh
GOGO_TEST_REQUIRE_SERVICES=1 go test ./tests/integration \
  -run '^TestIndependentServicesShareDatabaseHTTPAndTasks$' -count=1
```

If your default PostgreSQL installation is older than 16, set `GOGO_TEST_POSTGRES_BIN` to a supported server's `bin` directory. The test owns its disposable database and Redis; it never uses the example's `.env`.
