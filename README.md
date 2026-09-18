# Gogo

A Go backend framework under development with structured applications, its own ORM, an optional Admin module, and an optional Async task system. PostgreSQL and Redis are the initial external data backends.

## Documentation

Start with the [developer documentation](docs/index.md), [first-project tutorial](docs/guides/quickstart.md),
or [documentation build instructions](docs/README.md). Run `make docs-dev` and visit
[localhost:3000](http://localhost:3000) for the Docusaurus documentation site.
The three documentation tabs are **Docs**, **Admin**, and **Async**.
Quickstart covers installation, a basic project, running/building,
a database-backed API, and Docker on one page. Each feature page contains examples,
options, detailed contracts and its Go API reference. The [feature map](docs/guides/features.md)
links to the setup, examples, and limitations for each feature family.
To preview the production site with local full-text search, run `make docs` then
`make docs-serve`. Node.js 20+, npm, Python 3.10+, and Go are documentation-build
tools only; no Gogo server, PostgreSQL, Redis, or framework `.env` is needed.

## Alpha release

The rebuilt framework is published as **[v1.0.0-alpha.1](https://github.com/Newton-School/gogo/releases/tag/v1.0.0-alpha.1)**. This is a preview, not stable
1.0 or complete Django/Celery parity. See the [release and migration notes](releases/v1.0.0-alpha.1.md)
for all six module versions and compatibility boundaries.

```sh
go install github.com/Newton-School/gogo/cmd/gogo@v1.0.0-alpha.1
gogo startproject storefront --module example.com/storefront
cd storefront
go mod tidy
go run manage.go startapp catalog
```

Configure the generated `.env` before `check`, migrations or serving. Required
secrets have no built-in credential defaults. Pin this prerelease explicitly;
`@latest` may select the older stable implementation.

## Runnable showcase

Start with [examples/showcase](examples/showcase/README.md): a client using public
imports and this checkout's modules through `go.work` (also inside Docker).
It connects PostgreSQL models and migrations, authenticated Admin, a scoped
public API, forms, Redis cache/sessions and a separate Async worker.

The checkout removes Redis application namespaces: select a dedicated database
with `GOGO_REDIS_URL`, such as `redis://127.0.0.1:6379/1`. This breaking change is
not yet part of the published alpha. Existing prefixed Redis state is not
automatically migrated or deleted; read the [upgrade notes](docs/guides/connectors.md#upgrade-from-prefixed-keys).

Its [coverage map](examples/showcase/COVERAGE.md) groups 40 model kinds, 29 form
kinds, 21 widget configurations, 23 API serializer field constructors and
additional executable recipes by feature. It labels descriptor-only support,
compile-only examples and remaining gaps explicitly.

## Contributor checks

Go 1.26.8 or newer is required by every module and generated client. Use a
current patched toolchain: older Go 1.26 releases include filesystem-confinement
and HTTP/template security issues ([Go release history](https://go.dev/doc/devel/release)).
The Go command can select the required toolchain automatically; no global Go
installation change is required.

Run `make test` for workspace race tests and Admin JavaScript contract tests.
The latter use Node.js 20+ built-ins only, with no npm dependencies or browser;
`make test-js` runs them separately. Node.js is a contributor test dependency,
not a dependency of installing, building or running any public Go module.

`make test-modules` verifies the six public modules with workspace overrides
disabled and builds a fresh generated consumer. `make test-integration`
requires supported PostgreSQL and Redis tooling and uses owned disposable
fixtures; ordinary tests may explicitly skip unavailable local services.
Run `make vet build` for the workspace static and compilation checks.

`make audit-dependencies` runs the pinned Go vulnerability scanner against all
workspace modules and the selected Go standard library. It queries the live Go
vulnerability database and requires network access; it does not add a runtime
dependency to client projects. Reachable vulnerabilities fail the command.
Verbose output also lists advisories for unused packages inside required
modules; distinguish those from vulnerabilities affecting imported code. Record
the source revision and toolchain with results: a clean scan is dated evidence,
not a security guarantee or a replacement for code review and integration tests.

Independent-module tests use their packaged Gogo sources first, then the
configured `GOPROXY` for third-party dependencies (the public Go proxy by
default). An explicitly configured file proxy can reuse already downloaded
dependency artifacts during an outage; tests still get a fresh module cache,
workspace overrides stay disabled and dependency checksum verification is not
disabled. Report cached-dependency verification separately from a network install.
