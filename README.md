# Gogo

A Go backend framework under development with structured applications, its own ORM, an optional Admin module, and an optional Async task system. PostgreSQL and Redis are the initial external data backends.

## Alpha release

The rebuilt framework targets **v1.0.0-alpha.1**. This is a preview, not stable
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

## Architecture review

Architecture is maintained only through AgentFlow. Architecture version 2 is approved; implementation is in progress under the registered `gogo-framework-end-to-end` plan. Approval does not mean the framework is implemented or ready for production.

The review contains 128 feature flowcharts and 24 structured catalogs under Core, Admin, Async, Connectors, and Developer Tools and Operations. Start with **Review map → Developer journey**, then explore the owning feature trees. Nodes expose technical details; catalogs enumerate fields, options, interfaces, settings, and verification requirements.

The local review entrypoint is `.agentflow/index.html`. Regenerate it with:

```sh
make agentflow PROPOSAL=gogo-complete-framework-architecture
```

This renders the review and enables its project-local comment writer without opening a browser. Generated HTML and local runtime credentials are ignored by Git. Changes to architecture go through proposals and explicit review approval before framework implementation.

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
