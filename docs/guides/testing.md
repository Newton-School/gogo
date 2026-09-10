# Testing your application

Use normal Go tests. Gogo's explicit handlers, schemas, stores and providers let you test logic without starting an entire deployed application.

## Test layers

| Layer | What it proves |
| --- | --- |
| Schema/field tests | Valid and invalid declarations/values |
| Form/serializer tests | Binding, normalization, representation and input refusal |
| Handler tests with `httptest` | Route, status, headers, auth/CSRF and response behavior |
| ORM/backend tests | Actual queries, constraints, transactions and migrations |
| Async simulation | Deterministic task/workflow logic under an explicitly selected test backend |
| Real Redis worker tests | Provider-specific delivery/recovery integration |
| Browser and deployment tests | Actual browser interaction, accessibility and runtime topology |

Passing one layer does not prove the others. A compiled WebSocket or replica-routing example is not a live integration test.

## Commands

In your application:

```sh
go test ./...
go test -race ./...
go vet ./...
go run manage.go generate --check
go run manage.go makemigrations catalog --check
```

Replace `catalog` with your actual app labels. The framework's configuration-aware `build`/checks do not replace application tests.

## Test helpers

`async/testing` is an explicitly selected in-process simulator for tasks, results, calendars and workflows. It is not a durable production backend. `connectors/redis/testing` starts owned test infrastructure under its documented contract; it is not an application Redis service factory.

Never point integration tests at arbitrary application/production data. Use disposable databases/schemas/namespaces, validate ownership before cleanup, and avoid exposing fixture credentials in output.

## Documentation examples

From the framework checkout:

```sh
make docs-check
```

This checks the docs builder, compiles/runs documentation examples, validates the JavaScript syntax, generates the complete site and checks its local links/anchors and public-package guide coverage.

The scaffold tutorial also has a dedicated test that creates an owned temporary client, copies the documented files, generates descriptors/migration source and compiles the result. It does not connect to PostgreSQL or apply a migration.

The showcase's pinned public-module recipes are another useful starting point:

```sh
cd examples/showcase
GOWORK=off go test ./recipes/...
```

## Security and failure cases

Test wrong credentials, missing/forged CSRF, hidden rows, unauthorized relation IDs, invalid fields, oversized inputs, canceled operations, provider failure and uncertain commit/acknowledgement. Add concurrency/retry tests for invariants that depend on them. A successful happy path alone is not enough.
