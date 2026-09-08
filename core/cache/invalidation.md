# Exact after-commit invalidation

`NewInvalidation` freezes an explicit provider, namespace and at most 256 exact
keys. Use the same `cache.Key(namespace, version, dimensions...)` and provider
configuration as the read path. Constructor checks the namespace/digest shape;
it cannot prove which inputs produced a digest. It deduplicates keys in declared
order. Nothing scans Redis, expands a prefix, clears unrelated data or starts a
service. Applications own authorization; a namespace or hash is not a grant.

Inside the business transaction, call `plan.OnCommit(ctx, databaseAlias, false)`.
Successful registration is not successful invalidation. The callback runs only
after the actual outer commit on that alias. Rolled-back savepoints discard their
callbacks; released ones transfer them to their outer owner. Outside a matching
transaction, it runs immediately. Use the intended database alias, not merely the
alias of another ambient transaction.

`Apply(ctx)` is also available for explicit, privileged recovery. Its report
counts total unique keys, attempted Delete calls, and nil replies acknowledged.
An absent key can be acknowledged. A failed or panicking call can follow an
applied deletion, so the unacknowledged key is uncertain, not definitely present.
Processing stops on the first failure; later keys are not attempted. Neither
method retries automatically. Provider calls are cooperative and must respect
their context; this helper cannot forcibly stop them.

`InvalidationError` carries the same detached partial report. Routine formatting
does not expose keys or provider errors. `errors.Is`/`errors.As` allow deliberate
local cause inspection. A failed callback is reported through the existing
`db.CommittedCallbackError`; business data remains committed. Setting `robust=true`
allows later callbacks to continue, but neither hides this failure nor retries it.
A confirmed last Delete is not erased by cancellation immediately after its reply.

This is the **non-durable** branch. A process crash after commit can lose the
callback, and an earlier read-through loader can repopulate stale data after
deletion. Set a bounded TTL; strict-freshness reads must use authoritative storage.
This helper does not add durable intents, version-bump deduplication, distributed
stampede locks, invalidation/read transactions or automatic model signals. A
later explicit retry may delete newer cached content; it never rewrites the
business transaction. Opaque providers retain their own configuration and
lifetime contracts; the helper does not clone provider internals.
