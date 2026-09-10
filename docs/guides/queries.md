# Querying and saving

Create an `orm.Store` from your selected `db.Backend` and registered model schemas. Use `orm.For` with a typed model factory. Pass the request or task context into every operation that does I/O.

## Read records

Inside a function with `store *orm.Store` and `ctx context.Context`:

```go
products, err := orm.For(store, func() *Product { return &Product{} }).
    Filter(orm.Q("published", true)).
    OrderBy("name", "id").
    Limit(25).
    All(ctx)
if err != nil {
    return err
}
// products is []*Product; use only fields the caller may see.
```

This fragment belongs in your app package, where `Product` is declared. Build the store once in project-owned connection setup, not per request.

## Query operations

| Category | Public operations |
| --- | --- |
| Predicates | `Q`, `And`, `Or`, `Not`; generated/typed field references |
| Selection | `Filter`, `Exclude`, `Only`, `Defer`, `Distinct`, `DistinctOn` |
| Ordering and bounds | `OrderBy`, `Reverse`, `Limit`, `Offset` |
| Evaluation | `All`, `Get`, `First`, `Last`, `Count`, `Exists`, `Values`, `Iterator` |
| Related data | `SelectRelated`, prefetch declarations and relation managers |
| Expressions | `F`, `Value`, `Func`, arithmetic/conditional/cast expressions |
| Analytics | Aggregation, annotations, grouping/HAVING, window functions |
| Subqueries | Explicit scoped subqueries, correlated references and EXISTS |
| Concurrency | Row-lock options such as `SelectForUpdate`, `SelectForUpdateOf`, `SelectForNoKeyUpdate` |
| Diagnostics | `SQL`, `SQLContext` return compiled SQL and separate bound parameters |

The API reference lists exact method signatures. Advanced queries are subject to backend capability and compiler validation; Python QuerySet syntax is not accepted as Go code.

Queries are composed before evaluation. Use bounded pages or an iterator for large results. Always close an iterator and check `Err()` after iteration. Do not log bound parameters indiscriminately; they can contain private data.

## Save records

`store.Save(ctx, product, orm.SaveOptions{})` persists a model. Use `ForceInsert`, `ForceUpdate` or `UpdateFields` only when the desired write semantics are explicit. A failed or uncertain database operation must not be presented as a confirmed save.

`SaveOptions.Prepare` is the mutable normalization/validation hook immediately before encoding. `Guard` is a separate final read-only policy check. They may run more than once for fallback or inheritance writes, so callbacks must be safe to repeat.

> Direct saves do not automatically call `FullClean`. Bulk methods bypass ordinary save callbacks; choose them only when you have separately enforced the relevant validation and policy.

## Bulk writes and upserts

The package includes bulk create/update, query update, get-or-create/update-or-create operations and conflict-aware insertion support. Read the exact options and outcome contract before using one: batching, returning fields, uniqueness races and model-state refresh are distinct concerns. Use database constraints to enforce invariants under concurrency; an earlier existence check is not enough.

## Visibility belongs in the query

Include tenant, publication and ownership predicates before counting or paginating. Checking objects after the query is not sufficient: counts, ordering and page boundaries may already reveal hidden data. The [API resource](api.md) requires an explicit `Scope` for this reason.

Use [transactions](transactions.md) when multiple writes must succeed together and [transactional deletion](relations.md) for related-object effects.
