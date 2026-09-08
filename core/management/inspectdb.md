# Inspect an existing database

New projects register `inspectdb` on the central management command. Existing
projects can append `management.InspectDBCommand(resolve)` to `Project.Commands`.
The lazy resolver returns a configured `db.Backend` and `db.CatalogIntrospector`
for the selected alias, after the project's database resource opens.

```sh
go run manage.go inspectdb --database default --schema public --app legacy customer invoice
```

Review stdout, save the returned source in the app's model file, and run
`go run manage.go generate` to create typed field references and model
registrations. The command never writes a source file, registers models, runs
migrations, changes ownership, or adopts a baseline. Invalid flags/duplicate
selections fail before resources open; mapping failures produce no source.
Writer failures close resources and report failure, although an external writer
can already have received a prefix.

Flag values are validated as they are parsed. If an app label is a Go keyword,
put its distinct `--package` before `--app` (for example `--package legacy
--app type`); a keyword cannot be the default Go package name. The programmatic
options accept both values together without ordering.

Programmatic callers use `management.InspectDB(ctx, backend, introspector,
options)`. `RenderInspectedModels(catalog, options)` renders an already-inspected
catalog. Both return source for review; neither executes it. Catalog provider
diagnostics are checked alongside independent renderer validation.

| Output | Current behavior |
|---|---|
| Model structure | `models.Base`, exported Go fields, explicit physical column mapping, literal `Schema()` constructors compatible with `generate` |
| Ownership | Every descriptor has `Unmanaged: true`; this suppresses migration DDL, **not ORM writes or application authorization** |
| Schema binding | Exact table/column names are retained; configure the runtime connection to use the selected schema; automatic schema routing is not provided |
| Identity | Ordered database PK columns are preserved; no `id` is invented |
| Supported values | Integers/identity, bounded decimal, float, bool, char/text, UUID, date, default-precision timezone-aware datetime, JSON and binary; nullable fields use pointers |
| Generated values | Stored generated supported scalars preserve expression/element type and remain excluded from ORM writes |
| Relationships | Single-column, nondeferrable, same-schema FKs to another selected model; `NO ACTION` update; supported delete actions; unique source keys become one-to-one |
| Reverse names | Explicit deterministic names avoid collisions: `<source-model>_<field>_set`, or without `_set` for one-to-one |
| Constraints | Validated native CHECK/UNIQUE with representable deferral/null behavior; ordered PK/null metadata; original names/definitions also appear in escaped comments |
| Indexes | Valid/ready ordinary named indexes with default opclasses, column-matching collations, ascending/default null order, include columns, predicate and unique/null semantics |

Foreign-key paths use the current explicit ORM join workflow, for example
`SelectRelated("customer_id").Filter(orm.Q("customer_id__name", "Ada"))`.

Keyless relations, including views selected with `--include-views`, require
explicit developer-verified row identities. Repeat `--primary-key table.column`
for a composite identity. This declares that those existing columns are unique
and non-null; it does not inspect data to prove that claim and cannot replace an
existing database primary key. View models are unmanaged, not automatically
read-only; use application policy and database privileges for write restrictions.

Unsupported mappings fail with the table/column or metadata category. This
includes custom/domain/enum/range codecs, arrays, intervals with calendar
components, network-prefixed inet values, wall-time/time/precision-sensitive
temporal types, unconstrained/unusual numeric precision, virtual generated
columns, composite or cross-schema FKs, unsupported update/delete/deferral
actions, non-SIMPLE matching, expression/sorted/nondefault-opclass indexes,
index storage options/tablespaces, primary-key include columns, CHECK NO INHERIT, unvalidated/exclusion
constraints, and identifiers outside the current model/lookup grammar. Custom
codec source registration is still a follow-up; no mapping is guessed.

Raw defaults and definitions originate in the database and become trusted model
configuration only if a developer adopts the generated source. Inspection does
not execute them. Escaped comments retain backend distinctions the portable
descriptor does not model, such as native PK/FK constraint names and SQL type
spellings. Consequently, this output is not an exact schema-baseline comparison;
automatic migration adoption remains unavailable.

The renderer enforces its own bounded catalog/source limits and detects naming,
metadata and relation conflicts before returning output. It sorts model,
constraint and index output deterministically, preserves physical column order,
and leaves the supplied catalog descriptors unchanged.
