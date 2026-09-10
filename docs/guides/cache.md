# Caching

`core/cache` defines cache behavior independently of the backend. The Redis connector supplies the initial external adapter without importing Async.

## Use explicit keys and lifetimes

Construct `cache.Cache` with a configured store. Use `cache.Key(namespace, version, dimensions...)` to separate application/version/scope dimensions. Choose a TTL appropriate to the cached value and bound both payload size and work spent filling a miss.

Include tenant, user or permission-policy identity in keys when visibility differs. A cache hit must not return another caller's private data. Do not include raw credentials in keys; keys themselves can be visible to operators and diagnostics.

## Available operations

- Store get/set/delete and multi-key helpers.
- Namespaced/versioned keys and TTL-based values.
- Explicit cache wrapper and failure policy.
- Model-change invalidation hooks, with transaction-aware boundaries described in the technical guide.

Cache state is not the system of record. A miss, a provider outage and an invalid cached payload are different conditions. Choose whether an outage fails the operation or bypasses the cache; do not silently bypass a cache that is actually enforcing a security or consistency invariant.

## Invalidation

Invalidate only after the relevant write is confirmed according to the configured transaction path. An in-process signal alone is not durable cross-pod invalidation. Use shared Redis state and explicit version/key strategies when multiple deployments share the same data.

The showcase's `/cache-demo/` reuses a Redis-backed value for 15 seconds. It demonstrates basic cache behavior, not full-page caching or every invalidation strategy.
