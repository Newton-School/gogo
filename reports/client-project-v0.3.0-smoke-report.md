# Gogo Client Project Smoke Report

Date: 2026-06-30

Framework repo: `/Users/cybersaksham/Desktop/ns/gogo`

Client project: `/Users/cybersaksham/Desktop/ns/My_Gogo`

Framework source tested: current local branch with a downstream `replace github.com/Newton-School/gogo => ../gogo`.

## Summary

A fresh generated client project now works through the core Django-style workflow once the expected local `.env` is present.

Fixed in the framework during this pass:

- Fresh generated projects no longer fail `startproject` when best-effort module hydration cannot download an unpublished or temporarily unavailable version.
- Admin model URLs now render real Django-style list, add, change, delete, history, autocomplete, and JavaScript catalog responses instead of placeholder text.
- Admin login and password-change GET views now render HTML forms with Django-style IDs/classes.
- Admin add forms no longer show invalid History/Delete links, no longer produce double-slash delete URLs, and no longer render empty values as `&lt;nil&gt;`.
- Embedded admin CSS/JS are served under `/admin/static/admin.css` and `/admin/static/admin.js`, so generated projects render styled admin pages even when `/static/` is mounted to the project static root.

The env requirement remains expected and was not changed. Running `go run manage.go check` without `GOGO_SECRET_KEY` and `DATABASE_URL` still fails by design.

## Reset Performed

Removed and recreated the client project:

```bash
rm -rf /Users/cybersaksham/Desktop/ns/My_Gogo
go run ./cmd/gogo startproject My_Gogo ../My_Gogo
```

Because the local development CLI reports `0.0.0-dev`, the generated `go.mod` did not pin a release. For this smoke test only, I added:

```bash
go mod edit -require=github.com/Newton-School/gogo@v0.0.0
go mod edit -replace=github.com/Newton-School/gogo=../gogo
go mod tidy
```

## What Was Created Automatically

`startproject` created:

- `go.mod`, `manage.go`, `Makefile`, `.gitignore`, `.env.example`, `README.md`
- `.agent/rules/gogo.md` and `.agent/rules/gogo/*`
- `My_Gogo/app.go`, `admin.go`, `urls.go`, `queue.go`, `middleware.go`
- `My_Gogo/settings/base.go`, `local.go`, `test.go`, `production.go`
- `templates/`, `static/`, `media/`, `fixtures/`, `tests/integration/`
- Docker deploy files under `deploy/docker/`

`go run manage.go startapp notes apps/notes` created and wired:

- `apps/notes/app.go`, `models.go`, `admin.go`, `api.go`, `urls.go`, `forms.go`, `serializers.go`, `permissions.go`, `services.go`, `tasks.go`
- `apps/notes/tests/notes_test.go`
- `apps/notes/migrations/.keep`
- Project imports and registrations in `My_Gogo/app.go`, `admin.go`, `queue.go`, and `urls.go`

Framework commands created:

- `apps/notes/migrations/0001_initial.go`
- `db.sqlite3`
- `.gogo/auth_users.json`
- `.gogo/sessions/*`

## What Was Manual

Manual only for this local unreleased smoke run:

- Added local `replace` and `require` in generated `go.mod` to point at the checkout.
- Ran `go mod tidy` after adding the local replace.
- Created a local `.env` with safe development values so checks/server/database commands could run.

No app registration, URL registration, admin registration, API registration, queue task registration, or migration file was manually wired.

## Commands Verified

Passed:

```bash
go run manage.go check
go run manage.go startapp notes apps/notes
go test ./...
go run manage.go makemigrations notes
go run manage.go showmigrations
go run manage.go sqlmigrate notes 0001_initial
go run manage.go migrate
go run manage.go createsuperuser --username admin --email admin@example.com --password CorrectHorseBatteryStaple42 --noinput
go run manage.go dumpdata
go run manage.go dbshell
go run manage.go inspect --report
go run manage.go queues
go run manage.go worker --check
go run manage.go worker --once
go run manage.go beat --once
```

Expected env failure before `.env` existed:

```text
ERROR config invalid settings: GOGO_SECRET_KEY is required; DATABASE_URL is required
```

## HTTP Smoke

Server command:

```bash
go run manage.go runserver 127.0.0.1:8099
```

Passed:

- `/` returned `200` with `Welcome to My_Gogo`
- `/notes/` returned `200` with `notes index`
- `/api/notes/items/` returned `200` with JSON `{"count":0,"results":[]}`
- `/admin/login/` returned `200` with `<body class="login">` and `id="login-form"`
- Valid admin login returned `302`, set `gogo_sessionid`, and redirected to `/admin/`
- `/admin/` authenticated returned `200`, showed `Site administration`, `notes`, and user tools for `admin`
- `/admin/notes/item/` authenticated returned Django-style changelist HTML with `id="changelist"`, `action-checkbox-column`, and `searchbar`
- `/admin/notes/item/add/` authenticated returned Django-style add form with `id="item_form"` and save buttons
- Add form did not contain `/admin/notes/item//delete/`
- Add form did not contain `&lt;nil&gt;`
- `/admin/notes/item/1/change/` authenticated showed History and Delete object tools
- `/admin/notes/item/1/delete/` authenticated showed delete confirmation with `Yes, I'm sure`
- `/admin/notes/item/1/history/` authenticated showed object history table columns
- `/admin/static/admin.css` returned `200` with `text/css; charset=utf-8`

## Remaining Notes

- The generated project still intentionally requires a real `.env` for required settings.
- Local unreleased smoke testing requires a `replace` directive. Published releases should pin `github.com/Newton-School/gogo vX.Y.Z` automatically.
- Admin pages now render Django-style structure and styling, but persistence for add/change/delete forms remains a separate product capability from this UI rendering pass.
