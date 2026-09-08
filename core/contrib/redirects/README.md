# Site-bound redirects

`redirects` supplies a persisted model, explicit migration, bounded local-chain
lookup and optional HTTP handler. It belongs to Core and requires neither Admin
nor Async. Imports and constructors do not create tables, provision sites, open
connections or register an Admin interface.

## Install and mount

Register the `Site` and `Redirect` schemas in the application model registry.
Combine `sites.Migrations()` and `redirects.Migrations()` with the project's
migration executor; the redirect migration depends on `gogo_sites.0001_initial`.
Provision sites and redirects through an application-authorized write path:

```go
record, err := redirects.NewRedirect(siteID, "/old?edition=1", "/new")
if err != nil { return err }
record.Permanent = false // 302; the constructor defaults to permanent 301.
err = store.Save(ctx, record, orm.SaveOptions{
    ForceInsert: true,
    Prepare: redirects.Prepare,
})
```

`Redirect` stores UUID `id`, `site_id`, `old_path`, `new_path` and `permanent` in
`gogo_redirects`. A named unique constraint covers `(site_id, old_path)` and the
site foreign key cascades on deletion. Empty `new_path` means Gone. `Clean` is
explicit model/form validation; ordinary ORM `Save` does not call it. Use
`Prepare` for final normalization, and enforce mutation authority separately.
Bulk/raw writes must preserve the same invariants; lookup rejects malformed rows.

```go
resolver, err := redirects.New(redirects.Config{
    Backend: backend,
    AppendSlash: true,
    PreserveQuery: false,
})
if err != nil { return nil, err }
fallback, err := redirects.NewHandler(resolver, redirects.HandlerOptions{
    Sites: siteResolver, // Construct with explicit allowed hosts.
    Authorize: authorizeRedirectRead,
    // AllowExternal: authorizeExternalDestination, // Nil denies external URLs.
})
if err != nil { return nil, err }
router, err := urls.New(appRoutes...)
if err != nil { return nil, err }
return router.WithNotFound(fallback)
```

Configure both resolvers against the same logical site database and mount behind
the project's host/security middleware. This handler runs only at
the router's **final route miss**. It leaves matched views (including their 404s),
method denials and automatic OPTIONS untouched. This is narrower than Django's
[`RedirectFallbackMiddleware`](https://docs.djangoproject.com/en/5.2/ref/contrib/redirects/),
which can also intercept application-generated 404s.
No response buffering or second route-resolution pass occurs.

## Request and data flow

```text
Final route miss
  ├─ Method is not GET/HEAD → original/custom not-found handler; body never read
  └─ GET/HEAD → freeze request, validate URI/host and reject request bodies
       └─ Resolve selected site → authorize current application read access
            └─ Start owned read-only repeatable-read transaction
                 ├─ Same-alias ambient transaction → safe failure, no lookup
                 └─ Verify active site → exact site/path lookup
                      ├─ Missing → optional one-slash lookup → still missing: 404
                      └─ Found → validate bounded local chain
                           ├─ Cycle / invalid / DB / limit failure → rollback, 503
                           └─ Valid → commit read transaction → first-hop result
                                ├─ External → explicit destination grant required
                                └─ Reauthorize → empty target: 410; otherwise 301/302
```

`Authorize(ctx, sites.Info)` is required, runs before lookup and again before
emission, and must be a trusted **read-only** current grant check. Site info is a
content-selection snapshot, possibly cached, not authorization evidence. Use its
immutable ID to check current application authority. Site cache invalidation/TTL
remain the existing Sites contract; no cache is added for redirect records.
Inside the lookup snapshot the selected site ID is independently checked active.
Concurrent grant revocation requiring stronger atomic guarantees needs
application-owned synchronization; callbacks must not mutate grants or policy.

`AllowExternal(ctx, info, location)` separately authorizes the complete normalized
first-hop cross-origin location. Nil denies it. It has the same read-only contract.
Return the exact `ErrForbidden` for 403; other errors (including joined operational
errors), panics, timeouts and cancellation fail 503 without provider details or a
Location header. Redirects, Gone and generated errors are content-less, `no-store`
and `nosniff`. HEAD never writes a body. A custom `NotFound` owns its response/cache
policy, but still receives body suppression for HEAD and preserved caller context.

## Exact URI behavior

| Input / option | Behavior |
| --- | --- |
| `/old?b=2&a=1&a=3` | Exact escaped path and nonempty raw query; no sorting, merging or decoding for SQL keys |
| `/old?` | Same source key as `/old`; an empty incoming query is not distinct |
| `AppendSlash` (default false) | Only after a miss, retry `/old/?b=2&a=1&a=3`; never strip or clean a path |
| `PreserveQuery` (default false) | Copy the incoming query only if the destination has no query delimiter |
| Target `/new?own=1` or `/new?` | Its own query, including explicitly empty, wins; no merge |
| Target fragment | Retained in Location, excluded from next request and cycle identity |
| Relative target | Must start with exactly one leading slash; no browser-relative/authority-relative target |
| Absolute target | HTTP(S) only; canonical scheme/host/default port; same-origin stays in the local chain |
| Unicode path/query/fragment | Valid UTF-8 is percent-encoded to ASCII; existing valid escape spelling is retained |
| Limits | 2048 bytes both before and after escaping; default 16 local hops, configurable from 1 to 32 |

Invalid UTF-8/escapes, controls, backslashes, path dot-segments, encoded path
separators and recursively encoded structural path escapes are rejected. Query
values may contain encoded URLs as data. Raw ASCII outside the URI component
grammar must be percent-encoded. Origins require ASCII DNS/A-labels or canonical
IP literals; userinfo, zone IDs, malformed ports and browser-ambiguous short,
integer, octal or hexadecimal IPv4 spellings are refused. No DNS or network fetch
is performed. Request origin comes from validated `Host` and TLS/Core's trusted
proxy context, never raw forwarded headers or an unchecked absolute request URL.

## Direct lookup and transaction boundary

`Lookup(ctx, LookupInput{SiteID: id, URI: path, Origin: origin})` is a privileged
server API, not an HTTP authorization check. It returns `(Match, found, error)`;
every error clears both the result and `found`. Missing first records return
`found=false`; empty first targets return a found match with empty Location.

The lookup uses connector-neutral public contracts and bound ORM queries. All
local hops, including slash fallbacks, see one owned read-only repeatable-read
snapshot. Same-alias ambient transactions fail `ErrTransaction` rather than
silently using a weaker savepoint; unrelated aliases remain available in context.
Connectors must honor the requested transaction mode or reject it.

Cycle checks use effective query-preserved destinations and normalized comparison
identities, while database keys retain exact spelling. Repeated records/identities
fail closed. The configured hop count bounds matched redirects; one final bounded
lookup can establish a missing terminal target. The first redirect is returned,
not the last destination: later browser requests resolve their own routes. Remote
chains are not followed, so cross-site/remote cycles cannot be proven absent.
Routing is not called during traversal; chain validation is deliberately
conservative when a later URL might match a real application view.

No automatic Admin customization, async publication, negative cache, automatic
content provisioning or exhaustive Django middleware parity is implied.
