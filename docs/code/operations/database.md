# Database Operations

Gogo supports PostgreSQL as the production reference database and SQLite for
tests, local development, generated project smoke tests, and narrow
single-process deployments.

## Configuration

Set `DATABASE_URL` in every environment. The framework treats it as required.

Examples:

```bash
DATABASE_URL=postgres://gogo:password@db:5432/gogo?sslmode=require
DATABASE_URL=sqlite://./db.sqlite3
```

Keep database credentials in a secret manager. Do not commit `.env` files,
database dumps, local SQLite databases, or connection URLs containing passwords.

## Supported Engines

| Engine | Production status | Notes |
| --- | --- | --- |
| PostgreSQL 17+ | Recommended | Primary production target and CI integration database. |
| SQLite through `modernc.org/sqlite v1.53.0` | Limited | Use for local, test, and single-process deployments only. |

PostgreSQL-specific contrib packages can require extensions such as `pg_trgm`,
`btree_gin`, `btree_gist`, `hstore`, `unaccent`, `postgis`, and `vector`.

## Migrations

Use migrations as the only production schema change path.

```bash
go run manage.go makemigrations --check --dry-run
go run manage.go migrate --plan
go run manage.go sqlmigrate app_label 0001
go run manage.go migrate
go run manage.go showmigrations
```

Operational rules:

- `makemigrations` compares registered model metadata with historical migration
  state and writes operation specs for the actual model, table, field, index,
  and constraint changes. Database defaults, field-level `Unique`, and
  field-level `DBIndex` are represented as first-class migration state.
- Run migrations once per release.
- `go run manage.go migrate` takes a database-backed lock before applying
  schema or migration-history changes; a concurrent migration process fails
  before operations run.
- Take a backup before production migration.
- Review generated SQL for locks, table rewrites, destructive operations, and
  long-running backfills.
- Prefer nullable columns, backfills, then constraints across separate releases.
- Avoid editing applied migrations.
- Keep raw SQL reversible when rollback is required.
- Use `--fake` or `--fake-initial` only after manual inspection confirms the
  database already matches the migration state. `--fake-initial` records an
  initial migration only when declared initial tables, columns, primary keys,
  nullability, types, defaults, collations, indexes, and constraints match the
  live database where introspection is available; dialects that cannot inspect
  table shape fail closed.
- Use `--prune` only when stale migration records are understood and backed up.

## Existing Schema Adoption

Use the schema-adoption commands before allowing Gogo to own a database that
already has production tables.

```bash
go run manage.go inspectdb --table legacy_order
go run manage.go diffschema --app legacy
go run manage.go sqlmigrate legacy 0001_initial
go run manage.go migrate --app legacy --fake-initial
```

Adoption rules:

- Define exact app labels, table names, column names, primary keys, column
  types, database defaults, indexes, constraints, and relationship targets in
  project model metadata.
- Use `inspectdb` output as a reviewed starting point. It emits unmanaged
  metadata with explicit fields, dialect column types, defaults, indexes, and
  representable constraints, including unique physical indexes and foreign keys;
  relationship targets and product naming still need human review.
- Use `models.Unique` for simple table-level uniqueness. Use
  `models.NewUniqueIndex` for expression, partial, include, opclass, or
  method-specific uniqueness.
- For custom database types, use `models.FieldMeta.ColumnTypes`,
  `models/fields.NewCustomField`, or contrib helpers such as
  `contrib/postgres/vector`.
- For UUID, text, or other non-bigint relation columns, set relation field
  kinds or column types explicitly. Registered relation targets let migration
  state infer the target primary-key kind when the relation kind is omitted.
  Managed relation fields generate expected foreign-key constraints; unmanaged
  models keep metadata available without claiming migration ownership.
- Keep existing tables unmanaged until the team has reviewed the generated SQL
  and is ready for Gogo migrations to own future changes.
- Run `diffschema` against the live database and resolve any blocking drift
  before baselining.
- Baseline initial migrations with `--fake-initial`; it validates live table
  shape with the same comparator used by `diffschema` before recording
  migration history.
- After the baseline is recorded, use normal `makemigrations`, `sqlmigrate`,
  `migrate --plan`, and `migrate` for new schema changes.

## Connection Management

The app should use a bounded database pool sized for total web and worker
replicas. Keep database max connections below the database server limit after
reserving capacity for migrations, admin access, backups, and emergency shells.

Recommended checks:

- Connection acquisition timeout.
- Query timeout for request-scoped work.
- Separate long-running backfills from request handlers.
- Readiness check that opens and pings the database.
- Slow query logging in the database server.

## Backups

Backups must include application tables, migration records, auth tables, admin
logs, queue result tables when SQL results are used, and uploaded media metadata
stored in the database.

Minimum backup policy:

- Automated scheduled backups.
- Backup before every production migration.
- Restore test after backup configuration changes.
- Encrypted storage.
- Retention matching product and compliance requirements.
- Separate storage account or bucket from the primary database environment.

SQLite deployments must copy the database only when no writer is active or use
the database engine's backup API through project-specific tooling.

## Restore

A restore runbook must define:

- Target environment.
- Database owner and permissions.
- Extension setup.
- Restore command.
- Post-restore migration check.
- Application smoke tests.
- Queue and worker posture after restore.

After restore, run:

```bash
go run manage.go showmigrations
go run manage.go migrate --plan
```

If the plan wants to reapply migrations that should already be applied, stop and
inspect the migration recorder before allowing traffic.

## Rollbacks

Database rollback is safe only when the data shape is compatible with the old
application.

- Additive schema changes can often stay in place.
- Dropped columns, changed data types, destructive data migrations, and renamed
  tables usually require restore or a reviewed reverse migration.
- Worker processes must be stopped before rolling back schema used by tasks.
- Beat should be stopped before rolling back schedule storage changes.

Do not guess on destructive rollback. Restore from the verified backup when
compatibility is uncertain.
