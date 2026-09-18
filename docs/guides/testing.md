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

The scaffold tutorial has a dedicated test that creates an owned temporary client, copies the documented files from both tutorial chapters, generates descriptors/migration source and compiles the result. This test does not connect to PostgreSQL or apply a migration.

The separate database-backed check starts an owned PostgreSQL fixture, applies the generated migration, runs the documented seed twice, and verifies the real API's publication scope, persisted records, missing records and method denial:

```sh
GOGO_TEST_REQUIRE_SERVICES=1 go test -count=1 ./tests/integration -run TestDocumentationStorefrontPostgres
```

It requires PostgreSQL 16+ server tooling in PATH (or the explicit `GOGO_TEST_POSTGRES_BIN` directory). It does not use your application's `GOGO_DATABASE_URL`. The copied client test explicitly skips without the fixture; a skip is not a passed database integration. This harness uses local source replacements, not proof of a new published release.

### Test the tutorial's actual HTTP handler

Save this as `config/catalog_test.go` in the tutorial client. The fixture harness creates the schema, migrates it and runs the seed command before invoking this test. If you adapt it for your own application, supply only an owned disposable test database containing that seed; never point it at production. With no `DOCS_DATABASE_URL`, it skips explicitly.

{{code docs/snippets/storefront/config/catalog_test.go.txt}}

The showcase's pinned public-module recipes are another useful starting point:

```sh
cd examples/showcase
GOWORK=off go test ./recipes/...
```

## Security and failure cases

Test wrong credentials, missing/forged CSRF, hidden rows, unauthorized relation IDs, invalid fields, oversized inputs, canceled operations, provider failure and uncertain commit/acknowledgement. Add concurrency/retry tests for invariants that depend on them. A successful happy path alone is not enough.
