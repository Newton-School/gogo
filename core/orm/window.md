# Window annotations

Windows calculate across scoped rows while preserving each model row. Wrap a
supported function with `Window` and declare its decoded result using `Typed`:

```go
spec := db.WindowSpec{
    PartitionBy: []db.Expression{orm.F("team")},
    OrderBy: []db.WindowOrder{{Expression: orm.F("score"), Desc: true}},
}
query := employees.Annotate(map[string]orm.ResultExpression{
    "position": orm.Typed(orm.Window(orm.Rank(), spec), models.BigIntegerField("out")),
    "previous": orm.Typed(orm.Window(orm.Lag(orm.F("score")), spec), models.BigIntegerField("out", models.Nullable)),
})
rows, err := query.OrderBy("team", "position").All(ctx)
```

Results live in `row.ModelState().Annotations`; `Values` can select them.
`Typed` validates and decodes the output, not a SQL conversion; use `Cast` for
an explicit conversion. Decimal output remains decimal text, without floating
point conversion. NULL requires nullable output metadata.

| Function | Result and arguments |
|---|---|
| `RowNumber`, `Rank`, `DenseRank` | Integer position; rank ties share position, dense rank omits gaps |
| `PercentRank`, `CumeDist` | Floating-point distribution within the partition |
| `NTile(buckets)` | Integer bucket; positive 32-bit bucket count |
| `Lag(value, ...args)`, `Lead(value, ...args)` | Optional positive 32-bit integer literal offset, then optional default expression; absent arguments mean offset one and SQL NULL |
| `FirstValue`, `LastValue`, `NthValue(value, position)` | Value from the frame; positive 32-bit position; missing row returns NULL |
| Existing aggregate helpers | `Sum`, `Avg`, `Count`, `Min`, `Max`, `StdDev`, `Variance`, including row FILTER; DISTINCT window aggregates are unsupported |

Function arguments, partition expressions and window order expressions resolve
stored fields or scalar aliases. Related paths require explicit scoped
`SelectRelated`; window aggregates never infer collection joins. Bare window
functions without `Window` are rejected.

## Ordering and frames

The window's ordering is independent of the query's final `OrderBy`. No primary
key is silently added: unspecified ordering, or ties in a row-sensitive ordering,
can produce nondeterministic positions. Add a unique tie-breaker when desired;
doing so also changes which rows are peers.

A nil `Frame` retains native semantics: with window ordering the default runs
from the partition start through the current row's peers, not necessarily only
the current row. In particular, `LastValue` may not mean the partition's final
value. These are PostgreSQL's documented [window semantics](https://www.postgresql.org/docs/current/functions-window.html).

```go
spec.Frame = &db.WindowFrame{
    Mode: db.WindowRows,
    Start: db.WindowBound{Kind: db.WindowUnboundedPreceding},
    End: db.WindowBound{Kind: db.WindowUnboundedFollowing},
}
```

`WindowRows` also accepts `WindowPreceding`/`WindowFollowing` with a non-negative
`int64` offset. Offsets are bound parameters. `WindowRange` currently supports
only unbounded/current-row bounds; current-row boundaries include peers. RANGE
numeric/interval offsets are explicitly rejected pending their typed contract.
Invalid boundary kinds, direction ordering and contradictory null placement fail
before SQL; a valid same-direction offset pair may describe an empty frame.

An optional exclusion is `WindowExcludeCurrentRow`, `WindowExcludeGroup`,
`WindowExcludeTies` or `WindowExcludeNoOthers`. PostgreSQL always respects NULLs;
IGNORE NULLS and NTH_VALUE FROM LAST are not available. See the native
[frame syntax](https://www.postgresql.org/docs/current/sql-expressions.html#SYNTAX-WINDOW-FUNCTIONS).

## Execution boundaries

Caller-supplied root and related scopes run before window evaluation. SQL limit
and offset run after it. Construction snapshots the function/specification tree
and runs no provider callbacks; existing opaque runtime-value ownership rules
still apply. Cancellation, database failures and output decoding failures remain
errors, and no query result cache is shared by query clones.

The initial execution path supports ungrouped, unlocked annotations and their
outer ordering. It rejects grouped-window combinations, nested windows,
window-dependent WHERE/HAVING/CASE conditions, mutation assignments and terminal
aggregate expressions. Indirect aliases and predicate membership values cannot
bypass those rules. Filtering window outputs requires a future explicit subquery
execution path; it is never silently rewritten. `Count` on an annotated query
continues to count its rows through the existing outer-count query.

Connectors must advertise `window`, and separately `window_rows_frame`,
`window_range_frame`, and `window_frame_exclusion` when used. PostgreSQL advertises
these; a missing capability returns `db.UnsupportedFeature` before SQL. GROUPS
frames, named SQL windows, grouped-window execution and custom registered window
functions remain separate follow-ups.
