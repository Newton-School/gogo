# Explicit site selection

`sites` provides a persisted `Site`, explicit `Migrations()`, and a concurrent-safe
resolver. Imports and constructors never connect, create tables or seed a default.
Register `(&sites.Site{}).Schema()` and add `sites.Migrations()` to the project's
migration executor before provisioning sites.

```go
site, err := sites.NewSite("EXAMPLE.TEST.", "Example")
if err != nil { return err }
// This is privileged application provisioning, not an HTTP write endpoint.
err = store.Save(ctx, site, orm.SaveOptions{
    ForceInsert: true,
    Prepare: sites.Prepare,
})
```

The model stores a UUID, canonical domain, display name and active flag in
`gogo_sites`. A named unique constraint covers canonical domains. `NewSite`
allocates a UUID, normalizes the domain and starts active. `Site.Clean` also
participates in explicit FullClean/ModelForm validation. Ordinary ORM Save does
not run Clean: use the final `Prepare` hook when writing site configuration.
Direct/bulk writes remain privileged and must preserve the same invariant.
No automatic Admin registration or tenant permissions are installed.

```go
resolver, err := sites.New(sites.Config{
    Backend: backend,
    AllowedHosts: []string{"example.test", ".regional.example.test"},
    // SiteID: configuredUUID, // If set, preferred after host validation.
})
if err != nil { return err }
selected, err := resolver.WithCurrent(request)
if err != nil { return err }
current, _ := sites.FromContext(selected)
// Contrib/application queries explicitly filter on current.ID.
```

| Entry | Selection |
|---|---|
| `Current(request)` / `WithCurrent(request)` | Validate request Host; use configured SiteID or exact normalized domain |
| `ByID(ctx, id)` / `WithSite(ctx, id)` | Explicit UUID, including for background jobs; never guess the only site |
| `FromContext(ctx)` | Detached scalar snapshot; no request mutation, DB refresh or authorization |
| Missing, inactive, duplicate | `ErrSiteNotConfigured`; never select another site |
| Invalid/disallowed Host | `ErrInvalidHost` before cache or DB |
| DB/cache/provider failure or malformed row | `ErrUnavailable`, with no provider detail or partial result |
| Cancellation | Context error and zero result |

Site selection is **not tenant isolation**. A caller-controlled SiteID is not a
grant. Applications enforce permissions and include the selected site in every
site-owned query, cache key, email/task payload and other relevant boundary.
Context snapshots do not automatically refresh when site records change.

## Host policy

Request Host is checked locally even when security middleware runs first, and
even with an explicit SiteID. The resolver has its own copied allowlist; an empty
allowlist permits only explicit job/ByID selection. Exact names or a leading-dot
DNS suffix are supported; `*` is rejected. Forwarded-host headers are ignored.

Names use ASCII DNS spelling, including externally prepared IDNA A-labels;
Unicode-to-IDNA conversion is not provided. DNS case and one trailing dot are
normalized. Domain labels are bounded to 63 bytes and domains to 253 bytes. IP
literals are canonicalized; request IPv6 must be bracketed and zone IDs are
rejected. Stored domains use unbracketed IP literals and never contain a port.

A request port must be 1–65535 decimal digits, at most five characters, then is
ignored for site identity. Different ports on the same host cannot select different
sites. URLs, userinfo, whitespace, control characters, bad labels, invalid ports
and unbracketed IPv6 request authorities are rejected. No DNS lookup occurs.

## Optional cache

Caching is disabled by default. To enable it, supply `CacheConfig` with a Store,
an explicit project/database Namespace, nonzero Version, and TTL from one second
to 24 hours. A common backend alias such as `default` is **not** a sufficient
namespace across projects. Keys bind namespace, version, backend alias, lookup
kind and exact ID/domain. Configuration is copied at construction.

Only successful active-site results are cached. Entries are bounded to 8 KiB,
carry expiry and selection metadata, and are validated before use. Corrupt,
expired or identity-mismatched data is a miss; failed/missing-site loads are never
negative-cached. Same-process loads coalesce. Cache outages fail closed unless
`BypassUnavailable` explicitly permits loading from the actual database and
ignoring cache-write failure; bypass never substitutes a default site.

Ambient database transactions bypass **both** cache reads and writes. They see
their database snapshot, and uncommitted/rolled-back changes cannot populate a
shared entry. Commit callbacks are not installed.

Freshness is operator-owned: after committed site/domain/active changes, invalidate
the affected ID and old/new domain keys using `cache.Key(namespace+".sites",
version, backendAlias, kind, selector)`, or deploy a new configuration Version.
The TTL bounds ordinary staleness; cache storage is a trusted application resource,
not a place for untrusted writers. This package does not promise automatic global
invalidation or use site cache as an authorization store.
