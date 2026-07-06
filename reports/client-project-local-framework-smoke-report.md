# Client Project Smoke Report: Local Unpublished Framework

Date: 2026-06-30

Framework source: `/Users/cybersaksham/Desktop/ns/gogo`

Client project: `/Users/cybersaksham/Desktop/ns/My_Gogo`

CLI used:

```bash
go build -o /tmp/gogo-local ./cmd/gogo
/tmp/gogo-local startproject My_Gogo /Users/cybersaksham/Desktop/ns/My_Gogo
```

The generated client used a local module replacement:

```bash
go mod edit -replace github.com/Newton-School/gogo=/Users/cybersaksham/Desktop/ns/gogo
go mod tidy
```

## Automatic Generation

`startproject` generated the project module, `manage.go`, settings packages,
URL/admin/middleware/queue wiring, `Makefile`, deployment files, `.gitignore`,
`.env.example`, app/static/media/template/fixture/test folders, README, and
client agent rules under `.agent/rules/gogo.md` and `.agent/rules/gogo/*`.

`startapp notes apps/notes` generated models, metadata, admin registration,
database-backed API viewset wiring, serializers, forms, permissions, services,
queue task registration, HTTP routes, app resources, app tests, static/template
folders, and project updates for settings, URLs, API routes, admin, queue, app
configs, and model metadata.

## Manual Local Smoke Setup

The generated project intentionally failed before a local `.env` was added:

```text
invalid settings: GOGO_SECRET_KEY is required; DATABASE_URL is required
ERROR config invalid settings: GOGO_SECRET_KEY is required; DATABASE_URL is required
```

Manual files added for smoke testing:

- `.env` with local-only settings, SQLite database URL, port `127.0.0.1:18099`,
  static/media paths, and memory queue settings.
- `fixtures/notes_items.json` with one `notes.Item` record.

Manual unpublished-framework step:

- `go mod edit -replace ...` and `go mod tidy` were required only because this
  tested the local source without publishing a release.

## Passing Results

| Area | Result |
| --- | --- |
| Missing env validation | Passed; project refused to run without required env. |
| `check` | Passed after `.env`. |
| `startapp` auto-wiring | Passed; generated app was installed into settings, URLs, API, admin, queue, app configs, and model metadata. |
| Generated tests | Passed; app tests now verify metadata, registry resources, route, admin registration, form, serializer, task, and API route registration. |
| `makemigrations notes` | Passed; created `notes.0001_initial`. |
| `showmigrations` | Passed before and after apply. |
| `sqlmigrate notes 0001_initial` | Passed; rendered the `notes_item` create table SQL. |
| `migrate` | Passed; created the SQLite table and recorded migration state. |
| `createsuperuser` | Passed. |
| `changepassword admin` | Passed with Django-style positional username. |
| `collectstatic --dry-run` | Passed; reported `would collect 2 static files`. |
| `collectstatic` | Passed. |
| `shell --command` | Passed. |
| `dbshell --dry-run` | Passed; printed the resolved SQLite command. |
| Worker/beat/inspect/queues | Passed with memory broker/backend. |
| `optimizemigration` | Passed. |
| `squashmigrations` | Passed; squashed migration includes replacement metadata. |
| Squashed migration display | Passed; `showmigrations` marked the squashed migration as satisfied with `(replaces applied: 0001_initial)`. |
| `migrate --plan` after squash | Passed; reported no migrations to apply. |
| `loaddata` | Passed; fixture loaded into SQLite. |
| `dumpdata` | Passed; separate process read the loaded SQLite row. |
| Home/app routes | Passed via live server. |
| Generated API CRUD | Passed for list, create, detail, patch, and delete. |
| Admin login | Passed with cookie-backed CSRF token. |
| Admin changelist/add/change/history/delete | Passed against SQLite-backed model rows. |
| Missing admin object | Passed; change view returned `404` after delete. |
| Missing admin CSRF | Passed; unsafe POST without token returned `403`. |
| Server shutdown | Passed; port `18099` was clear after stopping runserver. |

## Fixed Since Previous Smoke

- Admin model pages now persist real database rows through metadata-backed CRUD.
- Generated APIs now register full database-backed model viewsets.
- Fixture commands now use a database-backed metadata fixture store in generated
  projects.
- Admin forms render non-empty CSRF tokens and reject missing-token POSTs.
- `changepassword admin` works in addition to `--username admin`.
- `collectstatic --dry-run` works and does not write output.
- `shell` and `dbshell` now report clear guidance when invoked without a command
  in noninteractive stdin contexts.
- Squashed migrations read and display replacement metadata, and already-applied
  replaced migrations satisfy the squashed migration for plan/show behavior.
- Generated app tests are no longer placeholder-only.

## Caveats

- Visual admin parity was verified through generated Django-style markup,
  classes, assets, and full CRUD flows, not by pixel-diffing against a live
  Django admin screenshot.
- The local `make ci` run passed. It printed Node engine warnings during
  `npm ci` because local Node is `v20.19.2`, but the docs check/build steps ran
  Astro with `node@22.12.0` and passed.
- `govulncheck` is not installed locally, so the CI target skipped the local
  vulnerability scan.
