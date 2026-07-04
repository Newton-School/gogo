# Migrations Reference

Migrations are Go values that describe schema and data changes. They update project state, execute database operations, and record applied history.

## Public Types

| Area | Types |
| --- | --- |
| Migration identity | `migrations.Dependency`, `migrations.Migration` |
| Operations | `migrations.Operation`, `migrations.SchemaEditor`, `migrations.ManifestOperation` |
| State | `migrations.ProjectState`, `ModelState`, `FieldState`, `IndexState`, `ConstraintState`, `TableSchema`, `ColumnSchema`, `IndexSchema`, `ConstraintSchema` |
| Graph | `migrations.Graph` |
| Autodetection | `migrations.Autodetector`, `DetectedChange`, `ChangeType`, `RenameQuestioner` |
| Loading and writing | `migrations.Loader`, `migrations.Writer` |
| Recording and execution | `migrations.Recorder`, `AppliedMigration`, `Executor`, `ExecutorOptions` |
| Safety | `migrations.SafetyOptions`, `SafetyCheck`, `SafetyCheckedOperation` |

## Migration Operations

Operations live in `migrations/operations`.

| Operation | Purpose |
| --- | --- |
| `CreateModel` | Create a model table, then model constraints and indexes, and update state. |
| `DeleteModel` | Delete a model table and state. |
| `RenameModel` | Rename a model in state and database. |
| `AlterModelTable` | Rename a model table. |
| `AlterModelTableComment` | Change table comment metadata. |
| `AlterModelOptions` | Change model options. |
| `AlterModelManagers` | Change manager metadata. |
| `AlterOrderWithRespectTo` | Change order-with-respect-to metadata. |
| `AlterTogether` | Change unique/index together metadata. |
| `AddField` | Add a field. |
| `RemoveField` | Remove a field. |
| `AlterField` | Alter column type, database default, nullability, and collation in deterministic order. |
| `RenameField` | Rename a field or column. |
| `AddIndex` | Add an index. |
| `RemoveIndex` | Remove an index. |
| `RenameIndex` | Rename an index. |
| `AddConstraint` | Add a constraint. |
| `RemoveConstraint` | Remove a constraint. |
| `RunSQL` | Run raw SQL with optional reverse SQL. |
| `RunGo` | Run Go data migration functions. |
| `SeparateDatabaseAndState` | Split database and state operations. |

## Flow

1. Build current `ProjectState` from model metadata.
2. Load historical migrations with `Loader`.
3. Compare states with `Autodetector`.
4. Write migration files with `Writer`.
5. Apply operations with `Executor`.
6. Store history in `Recorder`.
7. Validate consistency and safety checks.

Generated Go migration files are the source of truth for dependency,
replacement, and operation metadata. `migrate` and `sqlmigrate` load the
`Operations` values from the file before deciding which SQL to execute or
render; filename-based SQL is only a compatibility fallback for older placeholder
files with no operation metadata.

Models with `Managed` explicitly set to false remain available to ORM, admin,
API, serializer, and content-type consumers, but they are excluded from
migration state so Gogo does not create, alter, or drop their existing tables.

## Migration History Recorder

`Recorder` stores applied migration rows in `gogo_migrations` with app, name,
applied timestamp, checksum, and executor version. Recorder SQL is rendered
through the configured database dialect: PostgreSQL uses `$1` placeholders and
SQLite uses `?` placeholders. Applied history writes use `INSERT ... ON
CONFLICT(app, name) DO UPDATE`, not SQLite-only replacement syntax, so history
recording behaves consistently across supported databases.

`Executor` takes a database-backed migration lock before mutating schema or
history. The lock is stored in `gogo_migration_lock`; concurrent `migrate`
processes fail with `ErrMigrationLocked` before operations run. Plan-only
execution does not take the lock.

`migrate --fake-initial` records an initial migration without running its
database operations only when every table declared by its initial table
operations exists and the live columns match the expected initial schema,
including primary keys, nullability, type, database defaults, and collation
where the dialect can inspect them. When the initial migration declares indexes
or constraints and the editor can inspect them, those objects must also exist
and match. If any declared table, column, index, or constraint is missing or
mismatched, the migration runs normally or fails through the schema check
instead of silently baselining drift.

## Safety Checks

Safety checks detect destructive drops, non-null additions or tightenings
without defaults or acknowledgement, unique-constraint removals, irreversible
operations, unsafe SQL, and backend-specific hazards where operation metadata
exposes the required details.

## Error Types

`ErrInvalidMigration`, `ErrUnsafeMigration`, `ErrIrreversibleOperation`, `ErrDuplicateMigration`, `ErrMissingDependency`, `ErrMigrationCycle`, `ErrInconsistentMigrationHistory`, and `ErrMigrationLocked`.

## Example

```go
migration := migrations.Migration{
	AppLabel: "blog",
	Name:     migrations.InitialMigrationName(),
	Operations: []migrations.Operation{
		operations.RunSQL{SQL: `CREATE TABLE blog_post (id integer primary key, title text)`},
	},
}
err := migration.Validate()
_ = err
```

## Defaults, Indexes, And Constraints

Use `models.DefaultValue(value)` for literal database defaults and
`models.DefaultSQL("trusted_expression()")` for static SQL expressions. Gogo
normalizes these defaults into migration state and renders them for
`CreateModel`, `AddField`, and `AlterField`.

Field-level `Unique` and `DBIndex` are expanded into deterministic constraint
and index state. Explicit `models.Index` and `models.Constraint` metadata can
represent expressions, PostgreSQL methods, operator classes, include columns,
partial predicates, and foreign-key details where supported. `sqlmigrate`
renders this metadata from operation specs rather than guessing from filenames.

Simple field-only `models.Unique` constraints remain table constraints and keep
deferrable semantics. Unique declarations with expressions, predicates, include
columns, or opclasses become `AddIndex` operations with
`IndexState.Unique=true`, so PostgreSQL renders valid `CREATE UNIQUE INDEX`
SQL. Use `models.NewUniqueIndex` when the physical index shape is intentional.

Managed relation fields generate `foreign_key` constraint state when their
target model is registered. The generated constraint resolves `self`, app-local
model names, fully qualified `app.Model` labels, target primary keys, and
explicit `TargetFieldName` values. `protect` maps to SQL `RESTRICT`; `set_value`
maps to `NO ACTION` because setting arbitrary values is runtime behavior, not a
database FK action.

`CompareTableSchema` remains available for column-only compatibility.
`CompareTableShape` compares the richer `TableSchema`, including expected
indexes and constraints. `diffschema` uses the richer comparison and PostgreSQL
introspection reads catalog-formatted types, indexes, constraints, check
definitions, and foreign-key references.

`inspectdb` emits unmanaged `models.Metadata` snippets for adopting existing
tables. The output includes explicit `FieldMeta` entries, dialect `ColumnTypes`,
database defaults, indexes, unique physical indexes, and representable
unique/check/foreign-key constraints. Review the snippet before enabling writes
or generating migrations. When a foreign key can be mapped to a project relation
field, prefer the relation field as the owner; otherwise keep the inspected
foreign-key constraint metadata explicit.
