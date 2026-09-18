# Build a database-backed product API

Continue the **same `example.com/storefront` project** from [Your first project](quickstart.md). Keep its `Product` model and `/catalog/` route. This chapter adds real PostgreSQL records, an explicit write command, and a public read API; it does not expose anonymous writes.

## What you will build

| Request or command | Result |
| --- | --- |
| `go run manage.go seed` | Insert a public Notebook and a private draft in one transaction |
| `GET /api/products/` | List published products only |
| `GET /api/products/1/` | Read a published product by ID |
| Read a draft by its ID | Not found; the public API never reveals it |
| `POST /api/products/` | Method rejected; no create handler is registered |

Run commands from `storefront/`. The files below are complete. Replace files when directed; keep the generated app/migration registration and `config/connections.go` unchanged. If you chose a different module path, replace `example.com/storefront` in imports.

## 1. Choose the public representation

Replace `apps/catalog/serializers.go`:

{{code docs/snippets/storefront/apps/catalog/serializers.go.txt}}

The model's `Published` flag controls visibility but is not part of the public representation. Price stays an exact decimal string, not `float64`.

## 2. Declare the resource and row scope

Create `apps/catalog/api.go`:

{{code docs/snippets/storefront/apps/catalog/api.go.txt}}

`Policy` permits only viewing. `Scope` adds `published = true` **before** lookup, counting, and pagination. Do not fetch all rows and hide drafts afterward. `ReadOpenAPIRoutes` returns named read routes with schema metadata; it does not mount a schema URL by itself.

## 3. Write a transactional application service

Replace `apps/catalog/services.go`:

{{code docs/snippets/storefront/apps/catalog/services.go.txt}}

Validation is explicit because `Save` does not automatically call `FullClean`. The transaction context is passed into each query and save. Existing rows are left untouched when this single-operator seed command runs again. Do not use its read-then-insert pattern as a concurrent business idempotency mechanism; that requires database uniqueness and an explicit conflict policy.

## 4. Connect registered models to the database and router

Create `config/catalog.go`:

{{code docs/snippets/storefront/config/catalog.go.txt}}

There are two registries: the app registry holds contributed descriptors; the model registry validates the schemas used by the ORM. The store shares the already-open PostgreSQL backend. We build it at handler construction, not per request. The seed command builds one for its own invocation.

## 5. Select that handler and register the command

Replace `config/settings.go`:

{{code docs/snippets/storefront/config/settings.go.txt}}

`Connections.Resources` is still the generated connection owner. Both server and seed select `database`; migration and inspection factories remain registered. No global database connection or package-import side effect is needed.

## 6. Generate, migrate, seed, and run

```sh
go fmt ./...
go run manage.go generate
go run manage.go makemigrations catalog --check
go run manage.go migrate
go run manage.go seed
go test ./...
go run manage.go runserver
```

This chapter does not change the model from the previous chapter, so the migration drift check should report no changes. If you intentionally changed fields, run `makemigrations catalog`, inspect the generated migration, and then apply it.

The seed command prints `Tutorial products are ready.` In a second terminal:

```sh
curl -i http://localhost:8000/api/products/
curl -i http://localhost:8000/api/products/1/
curl -i -X POST http://localhost:8000/api/products/
```

The list is JSON containing Notebook and `19.95`, never Draft product. On a newly initialized tutorial database, Notebook is ID 1; otherwise use the ID from the list. Unsupported POST returns 405. A draft or nonexistent ID returns 404. A dependency failure is not an empty successful list.

## 7. Verify behavior before adding more features

Stop and restart the server: records should remain because PostgreSQL owns them. Run `seed` again with the server stopped; it must preserve existing records. Check an invalid ID and an unsupported method, not just the happy path.

The repository's tutorial check copies these exact files into a fresh generated client, generates model/migration code, and compiles the application. A separate real-PostgreSQL test verifies migrations, seeding, persistence, public row scope, and method denial. See [testing](testing.md) for how to run each layer.

Next: [package this application with Docker](docker.md). For authenticated writes, use the [API write guide](api-writes.md); for staff editing and queued jobs, follow [Admin setup](admin-wiring.md) and [Async wiring](async-wiring.md).
