# Explicit database routing

`db.NewRouter` freezes an explicit alias catalog and cooperative placement rules.
`orm.NewRouted(router, registry)` opts a Store into terminal-operation routing;
ordinary `orm.New(backend, registry)` keeps its existing behavior. The application
opens, configures and closes every provider. Construction checks declared aliases
but does not acquire a connection, run SQL, ping, or invoke a routing rule.

An explicit `Store.Using(alias)` or `Query.Using(alias)` bypasses rule `Select`,
never rule `Allow`. Otherwise the first nonempty selection wins, then the configured
default. All rules grant the actual selected alias. Errors never trigger another
selection, connection fallback or automatic write retry. Routing permissions are
placement permissions, **not** row authorization: retain explicit `WithScope`,
application object grants and write guards.

## Consistency and transactions

Replicas require `EnableReplicas` and a fresh `router.WithConsistency(ctx)` for
each request or unit of work. Without that context reads use a primary. A replica's
`Primary` is an application topology assertion, not discovered replication.
Automatic selection is constrained to its primary when required; an explicit
replica is refused instead of silently changed.

A write pins its primary family before hooks/defaults/first execution, including
failed, rolled-back, unknown-outcome and conservatively admitted no-op writes.
Pins never expire or downgrade during the request. A model cannot move to another
primary family within that context. Later sequential reads use the primary;
a read already admitted before a concurrent write pin may still finish stale.
There is no replica-lag estimate, TTL or concurrent linearizability guarantee.
At most 1,024 model/family associations are retained per context.

`Store.Atomic` selects one primary and carries a private witness to the actual
provider transaction. Every model terminal still receives its own placement
grant. Same-alias witnessed nesting uses savepoints; cross-alias or unmanaged
ambient transactions are refused, **including an unrelated backend claiming the
same alias**. Legacy `db.Atomic` alias-chain behavior is unchanged. A transaction
context is serial and valid only during its callback. The router preserves
existing commit, unknown-commit, rollback and after-commit callback outcomes; an
unknown result is not evidence of rollback and must be reconciled, never replayed
blindly. `models.State.Database` is diagnostic caller-owned data, not an automatic
route hint or permission witness.

## Supported and refused paths

| Routed operation | Initial support |
| --- | --- |
| Iterator, All, Get, First, Last, Exists, Count, Values | Local, single-table reads, scope and supported scalar expressions; explicit locks require routed Atomic |
| Aggregate | Local scalar aggregate expressions, with existing grouping/slicing limitations |
| Save, RefreshFromDB | Concrete single-table records; Save refuses schemas containing relations |
| Query.Update | Local single-table updates with fixed placement and owned transaction/savepoint |
| Store.Atomic | One primary; witnessed same-alias nesting only |
| SQL / SQLContext | Refused: SQL bytes alone cannot convey the selected executor |
| Prefetch / SelectRelated / relation traversal / subqueries | Refused, also when introduced by a scope callback |
| Bulk, upserts, Delete / collectors, relation managers | Refused before terminal model/scope/provider callbacks |
| ValidateUnique / ValidateConstraints / ValidateRelation | Refused; no hidden unrouted validation queries |
| Migrations | No routing policy yet; Apply, Reverse, History and SQL refuse the unbound facade before editor/lock/ledger callbacks |

`For` still calls its factory to construct and validate a query. Early refusal
means no **additional terminal-operation** factory, scope or provider effects.
Do not pass a routed Store's exported `Backend` to other framework services: it
is a non-dispatching facade, not an implicit default-primary escape. It does not
own pool cleanup. Use a separately configured explicit backend for unsupported
services; that is privileged application configuration, not automatic routing.

The router allows at most 64 named databases and 16 rules. Replica topology must
point directly to a declared primary. Selected backend handles must have comparable
identity and nonempty matching aliases. No discovery or cross-database FK/atomic
transaction support is implied. Actual migration admission, relation ownership,
bulk routing and instance provenance remain separate unsupported slices.

## Ownership

Supported terminals freeze the router/store selection, local schema and supplied
plain-data arguments before placement callbacks. Scoped predicates are detached
and checked again before compilation; later callbacks cannot replace the selected
Store.Backend or change the authorized persistence table. Opaque codec/provider
objects remain trusted application-owned configuration and must not be mutated
concurrently. No lock is held over policy callbacks or database I/O.
Save's model record remains intentionally mutable through its existing lifecycle:
only its operation options and authorized persistence schema are frozen. Use the
existing Prepare and Guard boundaries for proposed-value normalization and grants.

Lower-level privileged callers may use `Router.Resolve` and its `RouteBinding`.
Continue with `binding.Context()` to preserve the captured private routing and
transaction observations; the getter returns nil on an invalid binding. Opaque
application context values remain cooperative and cancellation stays live. The
bound backend performs final admission and dispatches to the exact witnessed
transaction or selected pool. Raw SQL possession is not a read-only SQL sandbox
or object-authorization capability.

The compiling example injects already configured providers and does not execute
without an application calling its helper. Native tests use distinct disposable
PostgreSQL schemas with intentionally different rows: role-routing proof, not
physical replication or network-failure simulation.
