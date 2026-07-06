# Gogo v0.2.0 Client Project Smoke Report

Date: 2026-06-30

Framework repo: `/Users/cybersaksham/Desktop/ns/gogo`

Client project: `/Users/cybersaksham/Desktop/ns/My_Gogo`

Installed CLI:

```bash
/Users/cybersaksham/go/bin/gogo
gogo 0.2.0 (commit unknown, built unknown)
```

## Summary

`v0.2.0` is a clear improvement over the previous client-project run. A new
project can be created, a new app can be auto-wired, the generated app exposes
HTTP routes, API routes, admin registration, forms, serializers, permissions,
static folders, template folders, tasks, and app metadata, and the development
server serves the app route, API route, admin index, and static files.

There are still framework-level gaps before the client workflow feels like
Django. The most important failures are migration application/state tracking,
squashed migrations breaking Go compilation, missing automatic `.env` creation,
missing initial Gogo module pin in `go.mod`, and admin being publicly readable
without login/auth enforcement.

## What Was Created Automatically

Created by `gogo startproject My_Gogo`:

- `go.mod`
- `manage.go`
- `Makefile`
- `.env.example`
- `.gitignore`
- `README.md`
- `.agent/rules/gogo.md`
- `.agent/rules/gogo/*` feature rule files
- `My_Gogo/settings/*`
- `My_Gogo/urls.go`
- `My_Gogo/admin.go`
- `My_Gogo/app.go`
- `My_Gogo/queue.go`
- `My_Gogo/middleware.go`
- `templates/base.html`
- `static/`, `media/`, `fixtures/`, `tests/integration/`
- `deploy/docker/Dockerfile`
- `deploy/docker/docker-compose.yml`

Created and wired by `go run manage.go startapp notes apps/notes`:

- `apps/notes/app.go`
- `apps/notes/models.go`
- `apps/notes/admin.go`
- `apps/notes/api.go`
- `apps/notes/urls.go`
- `apps/notes/forms.go`
- `apps/notes/serializers.go`
- `apps/notes/permissions.go`
- `apps/notes/services.go`
- `apps/notes/tasks.go`
- `apps/notes/tests/notes_test.go`
- `apps/notes/static/notes/.keep`
- `apps/notes/templates/notes/.keep`
- `apps/notes/migrations/.keep`
- Project import/wiring in `My_Gogo/app.go`, `My_Gogo/admin.go`,
  `My_Gogo/queue.go`, and `My_Gogo/urls.go`

Created by framework commands:

- `apps/notes/migrations/0001_initial.go` from `makemigrations`
- `apps/notes/migrations/0001_squashed_0001_initial.go` from
  `squashmigrations`
- `staticfiles/notes/site.css` from `collectstatic`
- `.gogo/auth_users.json` from `createsuperuser` and `changepassword`

## What I Had To Add Manually

- Ran `go mod tidy`; the generated `go.mod` did not initially pin
  `github.com/Newton-School/gogo`.
- Created `.env`; generated commands failed without `GOGO_SECRET_KEY` and
  `DATABASE_URL`.
- Added `apps/notes/static/notes/site.css` so static collection and dev static
  serving had a real app asset to test.
- Added `apps/notes/templates/notes/index.html` so the generated template folder
  had a real template asset.
- Added `fixtures/empty.json` so `loaddata` could be tested through a real file.
- Added `tests/integration/client_feature_test.go` to verify generated project
  HTTP/API/admin wiring, generated forms, serializers, permissions, queue task
  registration, canvas workflow creation, and in-memory worker execution.
- Renamed `apps/notes/migrations/0001_squashed_0001_initial.go` to
  `0001_squashed_0001_initial.go.disabled` after confirming it breaks project
  compilation.

## Commands Run

### Project Bootstrap

| Command | Result |
| --- | --- |
| `gogo version` | Passed, reported `gogo 0.2.0` |
| `gogo startproject My_Gogo` | Passed |
| `go mod tidy` | Passed, added `github.com/Newton-School/gogo v0.2.0` |
| `go run manage.go startapp notes apps/notes` | Passed |
| `go test ./...` after startapp | Passed before migration squash |

### Checks And Settings

| Command | Result |
| --- | --- |
| `go run manage.go check` before `.env` | Failed: `GOGO_SECRET_KEY` and `DATABASE_URL` required |
| `go run manage.go check` after `.env` | Passed |
| `go run manage.go check --deploy` | Failed with expected production hardening errors |

Deploy check errors were useful and specific:

- debug must be disabled
- secret key is not strong enough
- session cookies must be secure
- CSRF cookies must be secure
- HTTPS must be enabled
- migrations are not confirmed applied
- static files are not confirmed collected
- admin path has not been reviewed

### App, Forms, API, Admin, Permissions, Queues

| Feature | Evidence |
| --- | --- |
| Generated app route | `GET /notes/` returned `200` and `notes index` |
| Generated API route | `GET /api/notes/items/` returned `200` and `{"count":0,"results":[]}` |
| Admin index | `GET /admin/` returned `200` and listed `notes` / `Item` |
| Form validation | Integration test passed using `notes.NewItemForm` |
| Serializer validation | Integration test passed using `notes.ItemSerializer` |
| Permission helper | Integration test passed using `notes.CanViewItem` |
| Task registration | `go run manage.go inspect --report --ping` reported `registered=1` |
| Worker execution | Integration test sent `notes.example` through memory broker and worker stored `SUCCESS` / `ok` |
| Canvas | Integration test created and serialized a chord workflow |

### Static, Fixtures, Shell, Auth, Queue Commands

| Command | Result |
| --- | --- |
| `go run manage.go collectstatic` | Passed, `collected 3 static files` |
| `curl /static/notes/site.css` | Passed, served app CSS from development server |
| `go run manage.go createsuperuser ...` with weak/similar password | Failed correctly with password validation |
| `go run manage.go createsuperuser ...` with strong password | Passed |
| `go run manage.go changepassword ...` | Passed |
| `go run manage.go dumpdata --indent 2` | Passed, returned `[]` |
| `go run manage.go loaddata fixtures/empty.json` | Passed, `loaded 0 object(s) from 1 fixture(s)` |
| `go run manage.go shell --command 'printf shell-ok'` | Passed |
| `go run manage.go dbshell --dry-run` | Passed, printed `sqlite3 ./db.sqlite3` |
| `go run manage.go worker --check ...` | Passed |
| `go run manage.go worker --once ...` | Passed, reported no tasks |
| `go run manage.go beat --once ...` | Passed, enqueued 0 tasks for empty schedule |
| `go run manage.go queues` | Passed, reported `no queues found` |
| `go run manage.go queues --queue default` | Passed, reported `queue default not found` |

### Final Verification

These passed after disabling the broken squashed migration file:

```bash
go run manage.go test
make check
go mod tidy -diff
go run manage.go check
```

## Working Features

- CLI installation and `version`.
- Project scaffold.
- Client agent rule harness generation.
- App scaffold.
- Automatic app wiring into project app config, routes, APIs, admin, and queue.
- Generated model metadata.
- Generated form and serializer helpers.
- Generated permission constants/helpers.
- Generated route and API endpoint.
- Generated admin registration and admin index rendering.
- Generated queue task registration.
- Queue in-memory broker/backend worker execution through public APIs.
- Canvas chord serialization.
- Static collection and development static serving.
- Non-interactive auth management commands with password validation.
- Shell command execution.
- DBShell dry-run command resolution.
- Fixture dump/load command paths.
- Deploy checks with actionable hardening messages.
- Public generated project tests using only public Gogo packages.

## Not Working Or Still Weak

### 1. Generated Project Is Not Immediately Runnable

`go run manage.go check` fails immediately after `startproject` because no
`.env` exists and required values are missing:

```text
invalid settings: GOGO_SECRET_KEY is required; DATABASE_URL is required
```

This is correct validation behavior, but poor Django-like usability. A fresh
generated project should either create a development `.env` automatically or
print exact next-step instructions.

### 2. `go.mod` Does Not Pin Gogo Until `go mod tidy`

Generated `go.mod` initially contains only module/go/toolchain lines. A client
must run `go mod tidy` before project commands can resolve all framework
imports. For Django-like scaffolding, `startproject` should write the framework
module requirement directly.

### 3. Migration Apply/Recorder Is Not Actually Working

Evidence:

```bash
go run manage.go makemigrations
# created notes.0001_initial

go run manage.go showmigrations
# [ ] notes.0001_initial

go run manage.go migrate
# applied migrations on database default

go run manage.go showmigrations
# [ ] notes.0001_initial
```

`db.sqlite3` remained 0 bytes after `migrate`. The command reports success, but
the database schema and migration state are not changed.

### 4. `migrate --plan` Does Not Show Pending Work

With `notes.0001_initial` pending, `go run manage.go migrate --plan` printed
only:

```text
migration plan for database default
```

It did not list the pending migration.

### 5. `makemigrations --check --dry-run` Is Incorrect

After `0001_initial.go` already existed, this command printed:

```text
would create notes.0001_initial
```

It exited 0. The command should detect no model changes, or exit nonzero in
check mode when it would create a migration.

### 6. `squashmigrations` Breaks Go Compilation

`go run manage.go squashmigrations notes 0001_initial 0001_initial` created:

```text
apps/notes/migrations/0001_squashed_0001_initial.go
```

That file declares the same package-level symbol as `0001_initial.go`:

```go
var GeneratedMigration = gogomigrations.Migration{...}
```

Because both files are in the same Go package, `go test ./...` fails:

```text
GeneratedMigration redeclared in this block
```

I renamed the squashed file to `.disabled` so the rest of the project could be
tested.

### 7. `runserver :8111` Ignores The Positional Address

I started:

```bash
go run manage.go runserver :8111
```

The server still listened on `:8000` from `GOGO_HTTP_ADDR`. Either the command
should support the documented positional address override or reject unknown
positional args instead of silently ignoring them.

### 8. Admin Is Publicly Accessible

`GET /admin/` returned the admin index without login. This confirms the index
rendering works, but it is not production-grade admin behavior yet. The admin
site needs authentication, staff checks, sessions, CSRF handling, login/logout,
and permission enforcement before it matches Django expectations.

### 9. Auth Store Is Separate From Admin Runtime

`createsuperuser` and `changepassword` persist users to `.gogo/auth_users.json`.
That works for the CLI command path, but the admin UI did not use those users
for login or access control in this smoke test.

### 10. Fixtures Are Command-Level Only In This Test

`dumpdata` and `loaddata` work as command paths and now have clear empty output,
but they are not connected to real migrated database rows in this generated
client project because migration apply/persistence is not working.

## Recommended Framework Fixes

1. Make `startproject` produce a runnable dev project:
   - write `.env` with development-safe defaults, or
   - print a post-create next-step command that creates `.env`, and
   - write `github.com/Newton-School/gogo vX.Y.Z` into `go.mod`.
2. Implement real migration apply/recording for generated project migrations:
   - execute rendered SQL,
   - create/update migration recorder state,
   - make `showmigrations` read recorder state,
   - make `migrate --plan` list pending migrations.
3. Fix migration package generation:
   - each migration file needs a unique exported symbol, or
   - generated migration files need a registry function/list that can contain
     multiple migrations in one package.
4. Fix `makemigrations --check --dry-run` change detection and exit behavior.
5. Fix `runserver` positional address parsing or remove that documented usage.
6. Add admin auth enforcement:
   - login/logout routes,
   - session integration,
   - staff/superuser checks,
   - permission checks,
   - CSRF protection for mutating admin views.
7. Connect CLI-created superusers to admin authentication.
8. Add generated project end-to-end tests that start from `gogo startproject`
   and run the same client workflow used in this report.

## Current Client Project State

The client project is usable for HTTP/API/admin/static/queue smoke testing after
manual `.env` creation and after disabling the broken squashed migration file.

Important local artifacts:

- `.env` was created manually for smoke testing and is ignored by `.gitignore`.
- `.gogo/auth_users.json` was created by auth commands and is ignored by
  `.gitignore`.
- `db.sqlite3` exists but is empty because migrations did not actually apply.
- `apps/notes/migrations/0001_squashed_0001_initial.go.disabled` is preserved
  as evidence of the squash compile failure.
