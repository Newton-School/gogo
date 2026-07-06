# Client Project Smoke Report: Gogo v0.4.0

Date: 2026-06-30

Client project path: `/Users/cybersaksham/Desktop/ns/My_Gogo`

Framework CLI used:

```bash
/Users/cybersaksham/go/bin/gogo
gogo 0.4.0 (commit unknown, built unknown)
```

## Scope

This pass removed the previous client project, recreated it with the installed
`gogo` release binary, generated one app, exercised the project-local
management command surface, started the generated server, and verified the
home route, app route, API route, admin auth, admin model pages, and admin
static assets.

## Automatic Generation

`gogo startproject My_Gogo /Users/cybersaksham/Desktop/ns/My_Gogo` generated:

- `go.mod` requiring `github.com/Newton-School/gogo v0.4.0`
- `go.sum`
- project-local `manage.go`
- project settings under `My_Gogo/settings`
- project URLs, middleware, admin, and queue wiring
- grouped `.gitignore`
- grouped `.env.example`
- `Makefile`
- `README.md`
- `apps`, `fixtures`, `media`, `static`, `templates`, and `tests` folders
- Docker deployment files
- client AI-agent rules under `.agent/rules/gogo.md` and `.agent/rules/gogo/*`

`go run manage.go startapp notes apps/notes` generated:

- model metadata for `notes.Item`
- admin registration for `notes.Item`
- API route and serializer
- form helper
- permission helper
- service file
- queue task registration
- app URL route
- app tests
- app static and template folders
- automatic project updates for `InstalledApps`, routes, API routes, admin
  registration, and queue task registration

## Manual Client Work Needed

- A local `.env` was required before runtime commands could pass.
- The first `go run manage.go check` correctly failed without required env:

```text
invalid settings: GOGO_SECRET_KEY is required; DATABASE_URL is required
ERROR config invalid settings: GOGO_SECRET_KEY is required; DATABASE_URL is required
```

- For smoke testing, `.env` was created manually with local-only values:
  `GOGO_SECRET_KEY`, `DATABASE_URL=sqlite://./db.sqlite3`,
  `GOGO_HTTP_ADDR=127.0.0.1:8099`, static/media defaults, and memory queue
  settings.
- A fixture file was created manually at `fixtures/notes_items.json` to test
  `loaddata`.

## Passing Checks

| Area | Result |
| --- | --- |
| Project scaffold | Passed; clean client project generated from installed CLI. |
| Module setup | Passed; `go.mod` and `go.sum` generated with released module version. |
| Client agent rules | Passed; `.agent/rules/gogo.md` and feature rule files generated. |
| Required env validation | Passed; missing required env fails before runtime. |
| `check` | Passed after local `.env`. |
| `startapp` | Passed; app files and project wiring were generated automatically. |
| `go test ./...` | Passed; generated app test is currently placeholder-only. |
| `makemigrations notes` | Passed; created `notes.0001_initial`. |
| `showmigrations` | Passed before and after migration application. |
| `sqlmigrate notes 0001_initial` | Passed; rendered `CREATE TABLE IF NOT EXISTS "notes_item" ...`. |
| `migrate` | Passed; applied `notes.0001_initial`. |
| `createsuperuser` | Passed with `--username`, `--email`, `--password`, and `--noinput`. |
| `changepassword` | Passed with `--username admin --password ... --noinput`. |
| `dumpdata` | Passed; returned `[]` from the default fixture store. |
| `loaddata` | Passed parser/load path; reported `loaded 1 object(s) from 1 fixture(s)`. |
| `collectstatic` | Passed after local `.env` included `GOGO_STATIC_ROOT=staticfiles`. |
| `dbshell` | Exited successfully. |
| `shell` | Exited successfully. |
| `test` command | Passed and ran project tests. |
| `worker --check` | Passed with memory broker/backend. |
| `worker --once` | Passed; no tasks available. |
| `beat --once` | Passed; enqueued 0 tasks. |
| `inspect --report` | Passed; reported registered task and queue state. |
| `queues` | Passed; reported no queues found. |
| `optimizemigration` | Passed; no optimizations needed. |
| `squashmigrations` | Passed with required start/end arguments. |
| Home route | Passed; `GET /` returned `Welcome to My_Gogo`. |
| App route | Passed; `GET /notes/` returned `notes index`. |
| API route | Passed; `GET /api/notes/items/` returned `{"count":0,"results":[]}`. |
| Admin login page | Passed; rendered Django-like `body class="login"` and `id="login-form"`. |
| Admin login | Passed; valid superuser credentials returned `302` and session cookie. |
| Admin dashboard | Passed; rendered app/model table and user tools. |
| Admin app index | Passed; rendered `/admin/notes/`. |
| Admin changelist | Passed; rendered `#changelist`, search bar, actions, and result table. |
| Admin add form | Passed render check; rendered `id="item_form"` and Django-like submit row. |
| Admin change form | Passed render check for `/admin/notes/item/1/change/`. |
| Admin delete confirmation | Passed render check for `/admin/notes/item/1/delete/`. |
| Admin history page | Passed render check for `/admin/notes/item/1/history/`. |
| Admin password change page | Passed render check. |
| Admin static assets | Passed; `/admin/static/admin.css` and `/admin/static/admin.js` returned 200. |
| Forbidden imports | Passed; generated project does not import `github.com/Newton-School/gogo/internal`. |
| Server shutdown | Passed; port `8099` was clear after stopping runserver. |

## Issues Found

### 1. Admin model CRUD is not actually database-backed

The admin model screens render, but POSTing to `/admin/notes/item/add/` with
`name` and `slug` returned `200 OK` and re-rendered the add form instead of
creating an object, redirecting, and showing a success message. The changelist
still showed `0 items`.

The change, delete, and history pages also render for `/admin/notes/item/1/...`
even when no row exists, with empty field values. This is not Django parity.

Expected Django-like behavior:

- add form validates and saves a row
- successful add redirects according to `_save`, `_continue`, or `_addanother`
- changelist shows saved rows
- change/delete/history for missing IDs returns a proper 404
- change form loads actual object values
- delete removes the row
- history records real add/change/delete events

### 2. Fixture management is not database-backed in generated clients

`loaddata` successfully parsed and reported loading the fixture, but the
default management-command store is in-memory. A separate `dumpdata` process
does not see the loaded records, and fixtures do not populate the SQLite
database.

Expected Django-like behavior:

- `loaddata` writes rows into the configured database
- `dumpdata` reads rows from the configured database
- fixture serialization respects model metadata, primary keys, natural keys,
  and database aliases

### 3. API generation is list-only and stubbed

The generated API endpoint works, but it returns a hard-coded empty result set.
There is no automatic database-backed list/create/detail/update/delete API
surface for the generated model.

Expected Django-like behavior:

- list endpoint reads from database
- create/update/delete paths can be generated or configured
- serializers validate and persist model data
- permissions are enforced by request user/context

### 4. `changepassword` is not Django-compatible for positional username

`go run manage.go changepassword admin --password ... --noinput` failed because
the command only accepts `--username admin`. The flag form works, but Django
accepts a positional username.

Expected Django-like behavior:

- `changepassword admin` should select user `admin`
- `--username admin` can remain supported as an additional form

### 5. `collectstatic --dry-run` is missing

`go run manage.go collectstatic --dry-run` failed with an unsupported flag.
The command works with `GOGO_STATIC_ROOT` set, but Django supports dry-run
collection checks.

Expected Django-like behavior:

- support `--dry-run`
- report discovered files without writing to destination

### 6. `shell` and `dbshell` are placeholder-level

Both commands exited successfully but did not open an interactive shell or
database shell in this noninteractive smoke run.

Expected Django-like behavior:

- `shell` opens a useful Go-aware/project-aware REPL or documented fallback
- `dbshell` opens the configured database shell when the client binary exists
- noninteractive environments should emit a clear message instead of silent exit

### 7. Admin forms render empty CSRF tokens

Admin forms render `csrfmiddlewaretoken` with an empty value. That is acceptable
only for a prototype smoke test, not for a production-grade Django-parity admin.

Expected Django-like behavior:

- CSRF tokens are generated, stored, validated, and rotated according to
  framework settings

### 8. Squashed migration display is confusing in the one-migration case

`squashmigrations notes 0001_initial 0001_initial` created
`0001_squashed_0001_initial.go` and `showmigrations` listed it as unapplied
beside the already-applied `0001_initial`.

Expected Django-like behavior:

- squashed migrations should expose clear replacement metadata and migration
  graph behavior so users understand whether the squashed file replaces or
  adds to the existing applied chain

### 9. Generated tests are placeholders

`apps/notes/tests/notes_test.go` contains only a placeholder scaffold test.
The project test command passes, but it does not verify model metadata, routes,
forms, serializers, admin registration, API behavior, or queue task
registration.

Expected Django-like behavior:

- generated apps should include meaningful smoke tests
- tests should validate generated routes, admin registration, serializers,
  forms, task registration, and migration metadata

## Conclusion

The generated client project now boots and exercises much more of the framework
without manual code wiring. Project scaffolding, app scaffolding, env checks,
migrations, auth bootstrap, static collection, queue command wiring, route
wiring, API route registration, and Django-like admin page rendering are in
place.

The product is still not complete Django parity. The largest concrete gap is
database-backed model CRUD across admin, fixtures, and generated APIs. Until
that is implemented, the admin panel is visually close to Django but not
functionally equivalent to Django admin.
