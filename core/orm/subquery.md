# Correlated read expressions

`Subquery(inner, field)` selects one local scalar from an explicit
`inner.Limit(1)`. `Exists(inner)` is a boolean SQL expression, unlike the
terminal method `query.Exists(ctx)`. `OuterRef("id")` references the immediate
outer model's local stored field. These helpers execute no SQL at construction.

```go
inner := comments.WithScope(commentScope).
    Filter(orm.Q("article_id", orm.OuterRef("id"))).
    OrderBy("-created", "-id")

latest := orm.Subquery(inner.Limit(1), "body")
hasComments := orm.Exists(inner)
articles = articles.WithScope(articleScope).Annotate(map[string]orm.ResultExpression{
    "latest_body": orm.Typed(latest, models.TextField("latest_body", models.Nullable, models.Optional)),
    "has_comments": orm.Typed(hasComments, models.BooleanField("has_comments")),
})
```

The caller supplies a deterministic ordering when which matching row wins
matters. Gogo does not append a tie-breaker or silently add `Limit(1)`.
EXISTS intentionally discards projection and ordering, while retaining filters,
limit and offset, including `Limit(0)`.

## Scope, types and execution

Both queries must use the identical comparable backend implementation and the
same nonempty database alias. Merely sharing an alias is insufficient. Providers
whose values cannot be compared fail explicitly in this initial contract.
The operation freezes Store/backend/alias selection before context and scope
callbacks. The captured backend's service internals still obey its normal
concurrency and lifetime contract.

An outer `WithScope` requires an explicit inner `WithScope`. Each callback runs
against its own schema; there is no implicit scope inheritance or function
identity comparison. Different explicit policies are allowed. Deliberately
unscoped raw ORM queries remain possible when neither query declares a scope.
Applications remain responsible for authorizing that use. Scope callbacks can
add predicates but cannot introduce a new subquery after the operation's
backend-selection inventory has been frozen.

Construction snapshots built-in query data and metadata. Scope predicates are
snapshotted immediately after their callbacks; custom provider values retain
Query's documented trusted ownership. The inner SELECT is compiled into the
outer statement and uses the outer operation's selected executor, including an
existing same-alias transaction. It opens no connection or transaction itself.

The selected scalar's intrinsic value type and decimal precision are preserved;
automatic integer identities become ordinary integer outputs. A scalar output
is always nullable because an empty inner result is SQL NULL. Direct `Typed`
annotations must preserve that type/nullability. Use an explicit `Cast` for
conversion. Enclosing CASE/COALESCE expressions retain their existing explicit
output semantics. EXISTS is nonnullable Boolean. No source model defaults,
validators or hooks run inside SQL.

## Initial bounds

- One correlation level, at most 64 subquery occurrences, and the existing
  64-depth/8,192-node expression work bound across outer and inner queries.
  Ordinary parameter containers are scanned method-free with graph memoization;
  their scalar leaves do not count as expression nodes. Existing encoders retain
  ownership of ordinary value size/type/cycle handling.
  Expression-shaped ordinary data retains its type and ownership, but hidden
  query nodes in parameter Output metadata and extracted lazy `.Value` carriers
  are rejected before encoders run.
- One global placeholder sequence and the selected backend's parameter limit;
  unknown limits use 500. Query/schema identifiers are quoted, not raw SQL.
- Inner queries support local predicates, ordering and slicing. Joins, eager
  loading, annotations, DISTINCT, grouping, windows and row locks are refused.
- Outer support is ungrouped, unlocked read predicates, scalar expressions,
  annotations and ordering. Subqueries cannot define JOIN endpoints. Nested
  correlation, outer aliases/transforms/relationship paths, window expressions,
  mutations, terminal aggregate expressions and DDL placement are unsupported.
- Custom codecs, generated/relational fields and extension-specific scalar
  outputs are unsupported. Missing fields, unsupported dialect capability,
  malformed trees and parameter-data misuse fail before SQL execution.

The public `db.Subquery` is a resolved data-only connector contract. Direct AST
callers own its authorization predicates. The ORM does not accept a hand-built
resolved descriptor as a substitute for a lazy scoped ORM query. This feature
does not add FROM-derived-table construction, automatically filter window
outputs, or enable the separately refused grouped/sliced aggregate paths.

Read terminal error contracts are unchanged: check errors, including the
documented decoded prefix returned by `All`. This API does not add retries or
turn an uncommitted caller transaction into a durable result.
