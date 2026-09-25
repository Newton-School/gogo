# Set up a working Admin

Admin is a separately installed Go module. A model registration is only one part of setup: a working site also needs a database store, authentication, sessions, signing, CSRF, account routes, and migration registration.

## Start with the executable example

Use [the showcase](showcase.md) to see the complete integration before adapting it. In its directory:

```sh
docker compose up --build -d --wait
docker compose run --rm --no-deps web createadmin
docker compose run --rm --no-deps web credentials
```

The last command displays private bootstrap credentials; do not share its output. Open [the Admin login](http://localhost:8000/admin/login/), sign in, and open Products. This is a single-organization, staff-superuser example, **not** a multi-tenant authorization policy.

## Install in your client

```sh
go get github.com/Newton-School/gogo/admin@v1.0.0-alpha.2
```

For Redis sessions, also install the matching `connectors/redis` module. Add a private `GOGO_SECRET_KEY` (at least 32 bytes) and `GOGO_REDIS_URL` to your client settings in addition to PostgreSQL. The URL selects a dedicated Redis database, for example `/1`. When upgrading from alpha.1, follow [the Redis upgrade notes](connectors.md#redis-databases). Match keys in `.env.example` without copying secrets. A new environment variable alone does not open its service.

## Understand each file's job

| File in the example | Responsibility | Change for your application |
| --- | --- | --- |
| `config/apps.go` | Register business models, auth/content-type/Admin schemas and migrations | Your installed apps and schemas |
| `config/connections.go` | Open PostgreSQL and selected Redis role connections; close them on exit | Your DSNs, runtime resources and model registry |
| `config/accounts.go` | Build the account-aware Admin store and current staff policy | Tenant/organization/record scope and permitted account changes |
| `apps/catalog/admin.go` | Declare editable models and per-model behavior | Your fields, actions, relations and hooks |
| `config/urls.go` | Construct site, auth views and middleware; mount routes | Your public routes and branding |
| `config/commands.go` | Explicit bootstrap administrator creation | Your provisioning workflow; never reset users on startup |
| `config/settings.go` | Select required resources and register commands | Only resources each role needs |

These are real example files, not generated automatically by `startapp`. Replace `example.com/gogo-showcase` imports and remove unrelated Fieldlab/showcase routes when adapting. The [project structure guide](structure.md) explains which pieces the generator supplies.

## Register schemas before applying migrations

The complete registration below includes `auth.Schemas()`, content types, `admin.LogSchema()`, and their migration factories. Your ORM model registry and app migration registry must agree:

{{code examples/showcase/config/apps.go}}

Run `go run manage.go migrate` after this registration. Importing Admin does not create tables. Account permission synchronization/bootstrap is explicit; the showcase's `seed` and `createadmin` are custom commands, not built-in names every client gets.

## Construct the account-aware store

{{code examples/showcase/config/accounts.go}}

The store rechecks persisted account state and scopes every model. `QueryScope` protects reads; `ValidateWrite` protects proposed changes. These callbacks must enforce your actual domain rules. Do not replace them with unconditional success to make a page load.

## Mount login, staff pages, and session middleware

The complete handler below shows actual ordering and dependencies. The unrelated public routes are included because this is the example's real root handler, not a partial file with missing helpers:

{{code examples/showcase/config/urls.go}}

Read the request from outside to inside: host/security headers → timeout → session loading → current account identity → flash messages → route handler. Login/logout/password-change handlers are explicitly mounted; `/admin/` receives the registered site. The site's mutation handlers enforce CSRF. Do not wrap account identity outside session loading or treat a cookie's contents as a verified principal.

The [connection implementation](../../examples/showcase/config/connections.go), [security headers](../../examples/showcase/config/security.go), and [bootstrap commands](../../examples/showcase/config/commands.go) supply the dependencies used here. Run the complete example unchanged first; use this integration checklist when transplanting it into your client.

## Verify the boundaries

1. Signed-out requests cannot view model data.
2. A successful login uses the configured account store, not a hardcoded principal.
3. Invalid CSRF prevents changes.
4. Your policy denies unauthorized models and object IDs even if URLs are guessed.
5. Creating/changing a Product updates PostgreSQL; reloading the page retains the change.
6. Disabling a staff account or changing grants takes effect through current account checks.

Next: [customize lists, forms, and actions](admin-customization.md). For exact hooks and support limits, use [the Admin option map](admin.md#feature-and-option-map).
