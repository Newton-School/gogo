# Migrations

Migrations are versioned Go source that describes database changes. Gogo keeps a dependency graph and applied checksums. Running the web server does not automatically run migrations.

## Normal workflow

```sh
go run manage.go generate
go run manage.go makemigrations catalog
go run manage.go migrate --plan
go run manage.go sqlmigrate catalog 0001_initial
go run manage.go migrate
go run manage.go showmigrations
```

Use the actual generated migration name when it differs from `0001_initial`. Review generated source and SQL, then commit the migration and registry alongside the model change.

`migrate --plan` prints the source dependency plan; it is not a prediction of lock duration, data-loss risk or a production rehearsal. `showmigrations` reads applied history. SQL preview intentionally omits bound parameter values.

## Operations

| Area | Operations |
| --- | --- |
| Models | `CreateModel`, `DeleteModel` |
| Fields | `AddField`, `RemoveField`, `AlterField`, `RenameField` |
| Indexes | `AddIndex`, `RemoveIndex` |
| Constraints | `AddConstraint`, `RemoveConstraint` |
| Explicit database work | `RunSQL` with explicit reverse SQL |
| Data changes | `RunData` with stable ID and forward/backward callbacks |

Index and constraint capabilities belong to the selected backend. PostgreSQL-specific methods, concurrent operations and expression/partial indexes have separate validation and lifecycle rules. Do not assume every change is safely transactional or reversible.

## History and dependencies

`migrations.Migration` owns its app, name, dependencies and operation snapshot. Keep historical definitions stable. Changing current models must not silently change the meaning or checksum of an already-applied migration.

App dependencies, renamed fields and intermediary relations can affect ordering and generated state. Use the detailed dependency and schema-transition guides for those changes. Generated migration source is reviewable code, not a substitute for a backup.

## Check for drift

```sh
go run manage.go generate --check
go run manage.go makemigrations catalog --check
```

Run these in CI for each relevant app. They check generated source and declared migration state; they do not validate every live production database.

## Reverse deliberately

The migration command supports `--reverse` with an explicit target, including `app.zero`. Reversing can remove tables or data. Inspect reverse SQL and test restore/reversal on disposable data before running it on anything important. Do not automatically reverse a production migration merely because application startup failed.

`inspectdb` helps describe an existing database through an explicitly configured introspector. Its output is a starting point for review, not an automatic safe conversion of arbitrary schemas or older Gogo data.
