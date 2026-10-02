# Services

Keep independently deployable Go applications in `services/`. Share domain code through `apps/`; each service chooses its own routes, apps, resources and commands. One repository and one `go.mod` can produce many binaries.

`startservice` and `Project.MainPackage` are included in `v1.0.0-alpha.3`. Upgrade your CLI and all selected Gogo modules together before using these examples; no local module replacements are needed.

## Create

From your client project root, using the updated framework:

```sh
go run manage.go startservice sessions
```

The standalone updated CLI also supports `gogo startservice sessions` from a directory containing `go.mod`.

```text
backend/
├── go.mod
├── manage.go
├── config/
├── apps/
└── services/
    └── sessions/
        ├── main.go
        ├── project.go
        ├── urls.go
        ├── project_test.go
        └── README.md
```

Names start with a lowercase letter and contain lowercase letters, digits, hyphens or underscores, up to 63 characters. Empty segments, traversal, reserved device names, symlinked destinations and existing services are rejected. Generation does not rewrite `go.mod`, `manage.go`, `config/apps.go`, or your environment files.

## Run and build

Run from the client root, not inside the service directory:

```sh
go run ./services/sessions serve --addr=127.0.0.1:8001
```

The new service has dependency-free health routes:

```sh
curl http://127.0.0.1:8001/health/live/
curl http://127.0.0.1:8001/health/ready/
# {"status":"ok"}
```

No PostgreSQL, Redis, signing key, Admin or worker is installed implicitly.

```sh
go test ./services/sessions/...
go build -trimpath -o bin/sessions ./services/sessions
./bin/sessions serve --addr=127.0.0.1:8001
```

Build the package, not just `main.go`; the other files belong to the same `main` package. A deployed binary needs neither the Go toolchain nor source. Supply its runtime environment and any required assets separately.

## Entrypoint and reload

The generated entrypoint is deliberately small:

```go
func main() {
    gogo.Main(Project())
}
```

Its project declaration selects the build target:

```go
gogo.Project{
    Name:        "sessions",
    Root:        ".",
    MainPackage: "./services/sessions",
    Schema:      conf.CoreSchema(),
    // Handler: this service's explicitly configured router.
}
```

`MainPackage` controls `build` and development reload only. It does not load apps or select a production mode.

```sh
go run ./services/sessions build
# Creates bin/sessions; -o overrides the output.
go run ./services/sessions runserver --reload --addr=127.0.0.1:8001
```

An empty `MainPackage` preserves existing `manage.go` builds. `"."` builds the root package; `"./services/sessions"` builds that directory. Imports, absolute paths, patterns, filenames and traversal are rejected. Reload additionally rejects symlinked target directories. Run from `Root`; the shared `.env`, descriptors and watched files are resolved there. Changing the entrypoint requires restarting the supervisor.

Reload rebuilds the selected service—not the root backend—and uses the same preflight, graceful shutdown and failure handling as [development reload](detail-core-management-reload.md). It watches project sources, so shared-app edits also rebuild the service. The `build` management command validates runtime settings; plain `go build` requires no credentials.

## Select apps and routes

Import shared apps explicitly in the service's `project.go`:

```go
Apps: []app.Config{
    accounts.App(),
    billing.App(),
},
```

Include dependencies declared by `app.Config.Requires`. Installing descriptors does not automatically mount HTTP routes; construct the service's router explicitly. Keep app registration free of network I/O and background goroutines.

For larger services, add private implementation without another module:

```text
services/billing/
├── main.go
├── project.go
└── internal/
    ├── http/
    ├── workers/
    └── integrations/
```

Do not import another service's `main` or private `internal/` packages. Put reusable models, task contracts and business functions in shared packages. If an app package imports Admin or Async, those imports also enter every binary importing that app; split optional wiring into separate packages when build isolation matters.

## Select resources and configuration

Reuse existing URL names—there are no service-prefixed database variables:

```go
RuntimeResources: []string{"database"},
ResourceFactory:  connection.Resources,
```

The resource factory must actually open and close the selected resources; `RuntimeResources` alone does not install a connector. Share constructor code, not process-local connections.

```env
GOGO_DATABASE_URL=...
GOGO_REDIS_URL=...
```

Only a command selecting `database` requires the database URL; only one selecting `redis` requires the Redis URL. Supplied malformed values and unknown `GOGO_*` settings remain errors. Processes using shared custom settings should share their definitions too, preferably in a dependency-light package such as `config/settings` rather than importing a package containing the entire main backend factory.

Use [resource configuration](configuration.md) and [connector setup](connectors.md). Inject only the secrets each deployment needs. Database pool limits apply per process; multiply them by replicas when budgeting connections.

## HTTP between services

The runnable example uses a bounded client for a public demonstration endpoint:

```go
count, err := clients.Count(ctx, "http://127.0.0.1:8000")
if err != nil {
    return err
}
```

`clients.Count` is example application code, not a framework API. It uses a request context, five-second timeout, bounded response size and no redirects. Its endpoint is operator-configured, not taken from untrusted requests. For private APIs, add [authentication and authorization](auth.md); network location and shared database credentials do not establish caller identity.

Inside one process, call shared business functions directly. Importing another package does not send a request to its separately deployed process.

## Publish tasks and run workers

Register Async factories only in services that use them. HTTP producers and consumers must share the Redis logical database, queue name and versioned task contract.

```sh
# Separate deployments; commands exist only after Async factory wiring.
./bin/billing serve
./bin/billing worker --queues billing
```

A dedicated worker executable is equally valid:

```sh
./bin/worker worker --queues reports
```

Starting a web service never starts a worker or scheduler automatically. See [Async setup](async-wiring.md) for factories and resource selection. Make handlers safe for repeated delivery; use the [transactional outbox](transactions.md) when a database mutation and publication must be durable together.

## Migrations and deployment

Keep the complete model/migration graph in the root management project, then run one migration job before compatible service rollouts:

```sh
./bin/manage migrate
./bin/api serve
./bin/reports serve
./bin/worker worker
```

These are separate commands/processes, not a shell supervisor. Give each service its own deployment, replicas, resource limits and readiness checks. Deploy schema changes compatibly while older binaries may still be running. A service can share a database without requiring the other service's process to be alive.

## Complete example

The framework checkout includes `examples/services` with PostgreSQL-backed API and reports services, a Redis-only worker, shared migrations, a report command that exercises all three, and per-service Docker builds.

```sh
cd examples/services
make help
make build
```

For Docker, set a random hex `POSTGRES_PASSWORD` in this example's `.env`. It is a container-bootstrap input; Compose supplies the corresponding Gogo URLs.

```sh
docker compose up --build -d
docker compose exec reports /app/service report
```

On a fresh seeded database the report prints database count `2`, API count `2`, a task receipt, and worker result `4`. The local Compose stack shares loopback networking for the development Redis contract; use independently networked, authenticated deployments for production. See the example README for native commands and teardown.
