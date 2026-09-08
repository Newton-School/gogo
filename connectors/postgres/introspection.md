# Read-only catalog inspection

`postgres.Introspector` implements the optional public `db.CatalogIntrospector`
contract. This is the catalog foundation used by `management.InspectDB`; it does
not itself generate source or adopt migrations.

```go
catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{
    Schema: "public",
    Relations: []string{"customer", "invoice"},
    IncludeViews: false,
})
```

An omitted schema means the connection's current schema. An omitted relation
list selects tables, partitioned tables, and partitions; `IncludeViews` adds
views and materialized views. Explicitly selecting a missing or excluded object
fails. Names are exact, case-sensitive catalog names, including names which the
portable model identifier grammar cannot yet express. Input slices are not changed.

| Boundary | Behavior |
|---|---|
| Snapshot | One read-only repeatable-read transaction for the entire result |
| Queries | System catalogs only; system relations, functions, types, and operators are qualified independently of caller search path |
| Writes | No application-row reads, DDL, migration history, ownership markers, schema-path changes, or model registration |
| Identity | Exact schema/relation/column/constraint/index names; ordered composite keys and FK targets |
| Definitions | Database defaults, generated expressions, constraint and index SQL are observed text, never executed by inspection |
| Index details | Key versus included columns, expressions, predicate, method, uniqueness/null behavior, readiness, constraint ownership, ordering flags, collation and opclasses |
| Limits | 1,000 relations; per relation 1,600 columns and 1,024 constraints/indexes; 64 KiB per definition/comment; 8 MiB aggregate string metadata |
| Failure | Invalid selection, unsupported catalog kind, size overflow, query/close/commit failure, or cancellation returns an empty catalog and an error |

Collation and opclass strings are qualified SQL identifiers, quoted where
necessary, such as `public."name.with.dot"`. They are not dot-delimited paths.
No returned identifier or SQL fragment is authorization to execute it.

The `Field` mapping is deliberately separate from the native `DatabaseType`,
`TypeSchema`, and `TypeName`. Built-in scalar mappings preserve available
length/precision/nullability/PK/default metadata. Integer identity/serial columns
use auto kinds. Generated columns use `Generated` with a scalar/array `Element`.
One-dimensional built-in arrays retain their element descriptor.

Column, index, and constraint `MappingIssue` fields provide stable diagnostics for native behavior which the
current model source renderer cannot preserve. An empty diagnostic is not a
schema-equivalence guarantee; consumers must validate their own supported fields
and metadata independently.

Unknown, domain, enum, range, extension, multidimensional array, and `timetz`
types remain `Custom` without a codec; they never become text by guesswork.
A generated column can contain an unsupported `Element`. PostgreSQL unconstrained
numeric and unusual numeric scales remain observed numeric metadata, not valid
portable Decimal promises. Native temporal precision/time-zone behavior,
interval month components, arbitrary array values, and backend-only constraints
must likewise be checked by a source generator or explicit custom codec.

Catalog inspection is not a complete schema-equality proof. Table kind, raw
definitions, native types, and otherwise unrepresentable details must remain
visible to a developer; a generator must refuse unsupported mappings rather than
silently approximate them. Nothing here automatically marks a model managed or
adopts a database into migration history.
