# Gogo showcase

A standalone application pinned to **v1.0.0-alpha.1** of all six published Gogo modules. There are no local replacements; use `GOWORK=off`, including when running inside the framework checkout.

This is an alpha showcase, not a statement of Django/Celery feature parity. The field laboratory explicitly distinguishes validation, persistence, presentation and descriptor-only support. Unsupported features remain visible in its inventory.

Use the [coverage map](COVERAGE.md) to navigate features and their evidence levels.

## Run locally with Docker

Only Docker with Docker Compose v2.17+ is required. From this directory:

```sh
docker compose up --build -d --wait
```

Open [the showcase](http://localhost:8000/), [fields](http://localhost:8000/fields/),
[forms](http://localhost:8000/forms/) or [the API](http://localhost:8000/api/v1/products/).
The image builds against the published alpha, runs tests and migration-drift
checks, and includes the compiled application rather than a Go development server.
The first build needs internet access to download images and public Go modules.

The stack generates random private credentials, starts PostgreSQL and Redis,
then runs a separate `check` → `migrate` → `seed` initialization job before the
web process and worker. No host Go installation, database setup or `.env` editing
is needed. This profile deliberately does not inject the host `.env` into its
containers; the native setup below remains independent.

Create the administrator once, then explicitly display its generated credentials:

```sh
docker compose run --rm --no-deps web createadmin
docker compose run --rm --no-deps web credentials
```

Sign in at [Admin](http://localhost:8000/admin/). Creating it again fails instead
of resetting the password. You can change the password inside Admin; `credentials`
shows only the original bootstrap password, not a subsequently changed password.
Credentials are never printed by ordinary startup or application logs.

The worker is already running. Try its commands without a local Go toolchain:

```sh
docker compose run --rm --no-deps web demoasync task
docker compose run --rm --no-deps web demoasync group
docker compose run --rm --no-deps web demoasync chain
docker compose run --rm --no-deps web demoasync chord
docker compose ps
docker compose logs --tail=50 web worker initialize
```

Stop the whole stack with `docker compose down`. PostgreSQL data, Redis data and
generated credentials remain in project-scoped named volumes; the next `up`
preserves them. Removing volumes, including `down -v`, is a destructive reset of
this sample. Do not remove the credential volume while retaining the data volumes.

For source changes, run `docker compose down` followed by
`docker compose up --build -d --wait`. These commands preserve data and recreate
the whole shared network namespace. The image does not live-mount source or run
autoreload. If host port 8000 is occupied, change only the published port in
`compose.yaml`, retaining the `127.0.0.1` bind, and use that port in browser URLs.

### Showcase resource limits

Every container has a hard CPU quota, memory limit and process/thread limit in
`compose.yaml`. No additional environment variables are needed.

| Container | CPU cores | RAM | Processes/threads |
| --- | ---: | ---: | ---: |
| PostgreSQL | 0.75 | 512 MiB | 128 |
| Redis | 0.25 | 128 MiB | 64 |
| Web | 0.50 | 256 MiB | 128 |
| Worker | 0.50 | 256 MiB | 128 |
| Setup (one-shot) | 0.50 | 256 MiB | 128 |
| Initialize (one-shot) | 0.50 | 256 MiB | 128 |

The four long-running containers total **2 CPU cores and 1,152 MiB RAM** at
their limits. Setup finishes before PostgreSQL starts, and initialization finishes
before web/worker start. Additional `docker compose run` commands each inherit the
web limit and add to that total while running. Application `/tmp` mounts are capped
at 32 MiB and count toward their container's memory limit.

CPU use is throttled at the quota; a container that exhausts its memory can be
OOM-killed. Swap is disabled by setting `memswap_limit` equal to `mem_limit`, as
described in the [Compose resource settings](https://docs.docker.com/reference/compose-file/services/#memswap_limit).
These caps apply to runtime containers, not image builds, Docker Desktop's VM
overhead, other projects or persistent-volume disk usage. They are showcase
budgets, not production sizing or a guarantee against Docker Desktop hangs.

After changing limits, recreate the stack with `docker compose down` followed by
`docker compose up --build -d --wait`; restarting existing containers is not enough.
Neither command removes the named data volumes. Inspect runtime usage with
`docker stats --no-stream` after startup.

### Local-only container boundary

The alpha's development Redis connector requires a loopback host. PostgreSQL,
Redis, initialization, web and worker therefore share PostgreSQL's container
network namespace: both backend URLs use `127.0.0.1`, while the web process
listens on `0.0.0.0:8000` inside that namespace. Only HTTP is published, on host
`127.0.0.1:8000`; neither database is exposed to the host or LAN. Manage/recreate
this stack together rather than replacing PostgreSQL independently. Compose
dependency restart propagation is not a distributed recovery guarantee.

The web/worker run non-root with a read-only root filesystem. A short setup job
owns the credential volume; application roles mount it read-only. Anyone with
Docker-daemon access can read these local credentials. This is a single-user
development profile, not a production secret manager, scaling topology or TLS
deployment. It does not weaken the published Redis connector's checks.

Use `compose run ... web <command>` as shown: it invokes the credential-loading
entrypoint. A raw `docker compose exec web /app/manage ...` does not inherit the
credentials exported inside the main process and is not the supported command path.

## Run natively without Docker

Prerequisites: Go 1.26.8, a dedicated PostgreSQL database and Redis 7.2+ with `maxmemory-policy noeviction`. Development Redis must be loopback. Production connectors require their TLS/authentication/durability policies; this example is not a deployment recipe.

1. Copy `.env.example` to `.env` if needed. Keep `.env` private. Set `GOGO_DATABASE_URL`, `GOGO_REDIS_URL` and a random `GOGO_SECRET_KEY` of at least 32 bytes. Never use the example database for existing application data. `GOGO_SHOWCASE_SCHEMA` must already exist; it defaults to `public` in your dedicated database. Choose a unique `GOGO_REDIS_NAMESPACE` when sharing a Redis server.
2. Set `GOGO_SHOWCASE_ADMIN_IDENTIFIER` and `GOGO_SHOWCASE_ADMIN_PASSWORD` in `.env` for the one-time administrator command. Passwords require at least 12 characters and cannot be entirely numeric. No password is accepted as a command-line argument or printed.
3. Run these commands from this folder:

```sh
export GOWORK=off
go mod download
go run manage.go check
go run manage.go migrate --plan
go run manage.go migrate
go run manage.go seed
go run manage.go createadmin
go run manage.go runserver
```

Remove the bootstrap password from `.env` after creating the account. Open [the showcase](http://127.0.0.1:8000/), then sign in to [Admin](http://127.0.0.1:8000/admin/) with your own credentials. `seed` is repeatable and preserves existing products and identities; `createadmin` refuses to overwrite an account. Migrations run only when explicitly requested, never at server startup.

Use `go run manage.go version` and the other project commands here. The standalone
`gogo` CLI is for bootstrapping outside this configured application; its Core-only
schema rejects this application's custom environment keys.

For autoreload, use `go run manage.go runserver --reload`. `make build` performs required configuration and migration-drift checks before creating `bin/manage`. Configuration stays outside the binary; raw `go build` compiles code but does not validate runtime environment values.

## Developer map

```text
manage.go                         One command entrypoint
config/
  settings.go                     Typed required settings and project factory
  apps.go                         Explicit apps, model registry, migration graph
  connections.go                  Owned PostgreSQL and Redis role connections
  urls.go                         Root routes, auth/session/CSRF, health, cache
  accounts.go                     Persisted staff authority at trusted boundaries
  commands.go                     Explicit seed, createadmin and field inventory
  async.go                        Worker, relay, delayed dispatcher and demos
apps/catalog/
  models.go                       Product and cascading ProductNote relation
  serializers.go / urls.go         Public fields and published-only API visibility
  services.go                     Transactional, non-overwriting sample seed
  admin.go                        Lists, fieldsets, inline notes and publish action
  forms.go / views.go              Bound forms and real HTTP handlers
  tasks.go                        Typed, retry-safe arithmetic tasks and canvases
  migrations/                     Checked-in historical migration snapshots
  templates/ / static/             Application-owned HTML and CSS
apps/fieldlab/
  models.go                       Every model kind; persistable subset identified
  forms.go / widgets.go            Every form kind and available widget declaration
  catalog.go                      Machine-readable feature/evidence boundaries
  *_test.go                       Valid, invalid, escape and capability assertions
recipes/                          Executable public-API recipes for other services
```

## Try each surface

| Parent | Demonstration | What to inspect |
| --- | --- | --- |
| Models / ORM | `seed`; Product and Specimen Admin pages | Decimal precision, generated IDs, timestamps, validation, persisted scalar kinds and relations |
| Migrations | `migrate --plan`, `showmigrations`, `sqlmigrate catalog 0001_initial`, `makemigrations catalog --check` | Explicit history, SQL preview, repeat application and drift checks |
| API | `/api/v1/products/`, `/api/v1/products/<id>/`, `/api/schema/` | Explicit serializer fields, published-only scope, pagination, ETag and OpenAPI output |
| Admin | `/admin/` | Real password login, Redis sessions, CSRF, users/groups, readonly values, fieldsets, list edit/search/filter, inline notes, confirmation/action/history |
| Field laboratory | `/fields/`; `go run manage.go fields` | Parent-grouped model/form/widget inventory with exact limitations |
| Forms | `/forms/`; fieldlab tests | All non-file form kinds in browser; bounded File/Image multipart validation in tests; no implicit storage or mail |
| Cache | `/cache-demo/` | Timestamp reused for 15 seconds through Core cache + Redis; fail-closed provider errors |
| Operations | `/health/live/`, `/health/ready/` | Process liveness versus bounded PostgreSQL/session-store readiness |
| Async | Commands below | Separate durable Redis worker; typed tasks, delayed delivery, group, chain and chord |
| Other services | `GOWORK=off go test ./recipes/...` | Additional public-API examples with evidence level and explicit boundaries in their source |

The Admin authorization example is intentionally single-organization: only active staff superusers may access its records, and persisted account flags/version are checked at each trusted boundary. It does not demonstrate multi-tenancy. Public APIs cannot write or reveal unpublished products. File/image specimen keys are illustrative readonly strings, not existing uploaded objects. Identity/relation-only fieldlab tables are read-only in Admin; the seeded scalar specimen may be edited, not freely created with missing file values.

`GOGO_TRUSTED_PROXIES` accepts CIDR networks, not hostnames. Forwarded HTTPS/client IP are trusted only from those peers; leave it empty without a reverse proxy. `GOGO_DB_QUERY_TIMEOUT` bounds the whole ordinary web handler, including its database queries, with cancellation and a safe timeout response; it does not limit explicit CLI migrations. Core's broader environment template also lists optional services that this showcase does not wire (SMTP/reset delivery/private storage); setting those variables alone does not activate them.

## Background work

Run a second process from the same folder and environment:

```sh
GOWORK=off go run manage.go worker
```

In another terminal:

```sh
GOWORK=off go run manage.go demoasync task
GOWORK=off go run manage.go demoasync delayed
GOWORK=off go run manage.go demoasync group
GOWORK=off go run manage.go demoasync chain
GOWORK=off go run manage.go demoasync chord
```

Each command prints the accepted identity before awaiting results, with a 45-second bound. The worker runs both intent recovery and delayed dispatch. `task`/`delayed` return 42; `group` doubles 3 and 5; `chain` doubles 3 and then its output; `chord` sums both doubled values. A timeout does not revoke accepted work. Commands have local operator authority and are not exposed through HTTP. The tasks are pure and safe to deliver again; real external effects need application idempotency. This sample does not claim exactly-once effects, full Celery wire compatibility, periodic beat or every failure/recovery combination.

## Verification

```sh
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
```

The Docker image build also runs the Linux unit tests, vet and descriptor/migration
checks; it does not claim race or live-service coverage during image construction.
`docker/http_test.go` provides an opt-in black-box check of the running stack:
set `SHOWCASE_TEST_HTTP_URL=http://127.0.0.1:8000` and
`SHOWCASE_TEST_ADMIN_PASSWORD` to the current local Admin password, then run
`GOWORK=off go test -race ./docker`. It covers public pages, API visibility,
CSRF, wrong-password denial and authenticated Admin access without a browser.

Native tests are opt-in: set `GOGO_SHOWCASE_TEST_POSTGRES_DSN` to an explicitly disposable PostgreSQL database, then run:

```sh
GOWORK=off GOGO_TEST_REQUIRE_SERVICES=1 go test -race ./config -run TestNativeShowcaseJourney -count=1
```

The native test creates and removes only its own random PostgreSQL schema, starts its own Redis process, and verifies repeated migrations, non-overwriting seed, persisted model values, public visibility, wrong-password denial without gaining Admin access, successful login, forbidden Specimen creation, actual configured query cancellation and real task/canvas processing. It never uses the application `.env` database implicitly. Missing services skip the native test unless strict mode is enabled.

The framework's approved architecture is broader than this alpha release. Recipes marked compile-only are wiring examples, not exercised deployment proof. Full GIS, every Admin combination, every API/account workflow, every distributed failover scenario and complete Django/Celery parity are not certified by this sample.
