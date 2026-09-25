# Your first project

Create a project, add a model, generate a migration, and run the server. This is chapter one of a continuous tutorial: [run and build](running.md) → [database-backed API](tutorial-api.md) → [Docker](docker.md). Use [the Docker showcase](showcase.md) instead if you want a complete application to explore first.

## Before you start

- Go **1.26.8 or newer**, as required by this release's `go.mod`.
- A dedicated PostgreSQL database you are allowed to change.
- A terminal with Go's executable directory on `PATH`.

Redis, Admin and Async are not required for this first project.

Use [Install and prepare](installation.md) for CLI/PATH setup and a complete local PostgreSQL Compose file. Commands below assume a shell in your own projects directory; do not create the client inside the Gogo source checkout.

Create client projects outside the framework checkout. Its `go.work` is for framework contributors; if you intentionally work inside that checkout, use `GOWORK=off` for the independent client commands.

## 1. Install and create

```sh
go install github.com/Newton-School/gogo/cmd/gogo@v1.0.0-alpha.2
gogo startproject storefront --module example.com/storefront
cd storefront
go mod tidy
go run manage.go startapp catalog
```

Pin the alpha explicitly. Do not replace its version with `@latest`: the older stable implementation is a different, incompatible product line.

`startapp` creates `apps/catalog/` and registers the app in `config/apps.go`. Run subsequent commands through `manage.go` so they use your installed apps and settings.

Your checkpoint: `manage.go`, `go.mod`, `config/`, and `apps/catalog/` now exist. `go run manage.go help` lists commands without starting a server. If you use the local database container from the installation guide, create its Compose file in this new `storefront/` directory now.

## 2. Configure PostgreSQL

The generator creates a private `.env` and a shareable `.env.example`. Put your own database URL in `.env`:

```dotenv
# Database: replace the placeholders with your local database credentials.
GOGO_DATABASE_URL=postgres://YOUR_USER:YOUR_PASSWORD@127.0.0.1:5432/storefront?sslmode=disable
```

This URL is a format example, not a usable credential. The fresh generated project selects only the database resource, so `GOGO_DATABASE_URL` is its only required setting. Leave optional services unconfigured until you wire them. Plaintext database connections are for local development; see [connectors](connectors.md) before deployment.

## 3. Declare a model

Replace the generated `apps/catalog/models.go` with this complete file:

{{code docs/snippets/catalog/models.go}}

Use decimal strings for exact prices. `WithStructField` maps the public schema name to the actual Go struct field; it does not rename the database column. `models.Base` holds the model's persistence state.

## 4. Generate and migrate

```sh
go run manage.go generate
go run manage.go makemigrations catalog
go run manage.go check
go run manage.go migrate --plan
go run manage.go migrate
```

`generate` writes typed field references and registrations. `makemigrations` writes migration source and updates its registry. Read the generated migration before applying it. `migrate --plan` prints the source plan; it is not a transaction rollback simulation. `migrate` is the command that changes the database.

Do not edit an already-applied migration to change a model. Change the current model and create another migration.

## 5. Add a URL

Replace `apps/catalog/urls.go` with this complete file. The generated app already calls `Routes()` when it registers URL contributions.

{{code docs/snippets/catalog/urls.go}}

This route demonstrates HTTP wiring only; it does not query the product table. Read [Querying and saving](queries.md) and [REST APIs](api.md) to expose persisted records with an explicit visibility policy.

## 6. Run and verify

```sh
go run manage.go runserver
```

Visit [the application](http://127.0.0.1:8000/) and [the catalog route](http://127.0.0.1:8000/catalog/). The catalog response is `{"app":"catalog"}`.

For development reload on Linux/macOS:

```sh
go run manage.go runserver --reload
```

Run these checks after changing models:

```sh
go run manage.go generate --check
go run manage.go makemigrations catalog --check
go test ./...
```

## Continue this project

The `/catalog/` route proves request routing, not database-backed API behavior. Next, [run and compile the application](running.md), then [connect your Product to a real API](tutorial-api.md). That chapter provides every changed file, writes sample records, and verifies that private drafts are excluded. Finish with [Docker](docker.md).

## Feature guides

- [Query and save Product records](queries.md).
- [Expose a scoped API](api.md).
- [Register Product in Admin](admin.md).
- [Run a background task](async.md).
- [Understand alpha limitations](compatibility.md).
