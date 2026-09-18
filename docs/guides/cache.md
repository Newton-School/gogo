# Caching

`core/cache` defines cache behavior independently of the backend. The Redis connector supplies the initial external adapter without importing Async.

## Use explicit keys and lifetimes

In your project-owned handler factory, after opening a `connectors/redis` connection with `CacheRole`, construct one cache wrapper. Import `core/cache` and alias `connectors/redis` as `redis`:

```go
productsCache := &cache.Cache{
    Store: &redis.Cache{Connection: cacheConnection},
    TTL: 30 * time.Second,
    BypassUnavailable: true, // Only for optional cached data, never authorization.
}
```

Inside a request handler, with that initialized wrapper:

```go
body, err := productsCache.GetOrSet(r.Context(),
    cache.Key("catalog", 1, "public", "page-1"),
    func(ctx context.Context) ([]byte, error) {
        products, err := orm.For(store, func() *Product { return &Product{} }).
            Filter(orm.Q("published", true)).OrderBy("id").Limit(25).All(ctx)
        if err != nil { return nil, err }
        // Build an allowlisted representation; never cache every model field.
        names := make([]string, 0, len(products))
        for _, product := range products { names = append(names, product.Name) }
        return json.Marshal(names)
    })
```

Handle `err` before writing `body`. This fragment uses the tutorial's `Product`, an initialized ORM `store`, and `encoding/json`, `context`, and `time`. A successful value is a JSON array of public names. A second request within the TTL can reuse it. Change the key dimensions when filters, locale, tenant, or visibility change; these fixed dimensions are only for this fixed public page.

For a runnable Redis example, start the [showcase](showcase.md) and request `http://localhost:8000/cache-demo/` twice within 15 seconds; `generated_at` should remain unchanged. The implementation is in its [root handler](../../examples/showcase/config/urls.go).

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
