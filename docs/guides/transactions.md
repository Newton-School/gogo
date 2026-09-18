# Transactions and database routing

Use `db.Atomic` when a logical change must commit or roll back as one unit. Pass its callback context into every ORM call inside the transaction.

## Atomic writes

```go
err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(txctx context.Context) error {
    if err := store.Save(txctx, product, orm.SaveOptions{}); err != nil {
        return err
    }
    return store.Save(txctx, note, orm.SaveOptions{})
})
```

This is a wiring fragment: `backend`, `store`, `product` and `note` must already be configured by your application. Return failures from the callback; swallowing an error can allow later code to continue toward commit.

Nested operations on the same alias use savepoints. `AtomicOptions.Durable` requires the outermost transaction. `MarkRollback` marks an ambient transaction rollback-only. `ExecutorFor` returns the correct executor for the current context and routing boundary.

> A transaction context represents a serial unit of work. Do not share it across concurrently running goroutines. Go concurrency remains available outside that transaction boundary.

## After-commit work

`db.OnCommit` runs a callback after the applicable transaction succeeds. With no transaction, it runs immediately. An ordinary callback is not durable job delivery: the process can fail between commit and execution.

For durable database-to-queue coupling, use Async's explicit transactional outbox and register its migration. The outbox records intent inside the transaction; a relay publishes committed rows later. See [Scheduling and recovery](scheduling.md).

## Multiple databases

`core/db` defines backend/executor contracts and explicit routing. `core/orm` composes model queries with that routing policy. Select primary/replica roles, aliases and consistency requirements deliberately; a configured replica is not a guarantee of fresh data.

Routing must enforce both reads and writes, relation targets, transactions and migration ownership. It must not allow a read-only route to become a write path through a raw executor. Failover, replication and cross-database atomicity are infrastructure concerns, not promises made by the routing API.

The technical routing guide and ORM routing recipe show the exact composition. That sample recipe is compile-only, not a running replication demonstration.
