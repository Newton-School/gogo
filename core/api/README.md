# API resources

`NewResource` exposes explicit read-only list/detail handlers. Put its named
`Routes("products/", "product")` in an app's `urls.go` with `urls.Include`.
Authentication comes from the surrounding session, bearer or application
middleware. Anonymous access requires both `AllowAnonymous` and a permitting
policy. Tokens retain their scope ceiling even with a custom policy.

Required configuration is a registered `Model` key, ORM `Store`, output
`Serializer`, permission `Policy`, and `Scope`. Scope must encode **all row
visibility**, including tenant and object rules. It applies before filter,
search, count, page selection, and separately to eager-loaded target schemas.
Object policy checks are an additional defense, not a substitute for scope:
they cannot remove hidden rows from aggregate counts or navigation metadata.
`IncludeCount` is opt-in and uses that same scoped query; counts and offset
pages are separate reads, not a transactionally consistent snapshot.

`FromModel` requires a field allowlist. Read-only does not mean secret: use
write-only/hidden fields or omit them. `AllowField` can further remove fields
before reading or computing them. Nested data needs its own explicit serializer
and relation representation policy. Models are never exposed automatically.

`FilterConfig` maps public parameter names to declared scalar fields/operators.
IN and range use repeated values (`?ids=1&ids=2`), while ordering uses a comma
list of allowlisted field names (`?ordering=-name,id`). Search combines words
with AND and configured text fields with OR. Query operands are typed but do
not execute model save validators; partial email/URL/text operands are valid.
Ordering always adds every primary-key component as a unique tie-breaker.

Pagination supports page-number (`page`, `page_size`), limit/offset (`limit`,
`offset`), and forward-only signed cursors (`cursor`, `page_size`), default size
50 and maximum 200. Invalid, mixed, duplicate or undeclared parameters return
400. Navigation is relative and does not trust the request Host. Requests and
generated navigation have the same 16 KiB query budget; an over-budget link
fails the whole response instead of returning unusable navigation.

For `pagination.CursorMode`, supply `ResourceCursorOptions` with a purpose-bound
`security.Signer`, unique resource `Name`, semantic `Version`, explicit
`ImmutableFields`, and a `ScopeIdentity` callback returning the current
tenant/visibility-policy version. Every allowed ordering field and every
primary-key component must be a non-null scalar, guaranteed immutable across
**all** writes, and directly exposed by the serializer. A field policy that
hides a cursor key fails navigation closed. Positions are signed, not encrypted;
never configure secret values as cursor keys.

Cursors bind that resource/version/scope, the verified principal and permission
ceiling, filters, ordering and page size. Expired, forged or rebound cursors
return 400 before querying model rows. Keyset comparisons preserve exact large
integer and decimal values and add primary keys to break ordering ties. Earlier
insertions do not shift subsequent pages, but results are not a database
snapshot: concurrent inserts, deletes and visibility changes can change the
traversal. `IncludeCount` counts the full scoped filtered collection in a
separate read. Cursor navigation currently returns only `next`, not `previous`.

Missing or out-of-scope details return 404. For custom detail decoders return
`ErrInvalidKey` for malformed input; retain provider/context errors so failures
are not presented as absence. Denied request policies do not query the model.
Object or representation failure returns no partial list. Responses are JSON,
private/no-store, and support GET/HEAD; an unacceptable Accept header returns
406. Callback panic values, SQL, and provider error text are not rendered.

Explicit JSON creation is available through [CreateHandler](create.md), with
typed model hooks, mandatory write/audit policies and confirmed transaction
outcomes and opt-in [durable operation receipts](idempotency.md). Configure
`CreateIdempotencyOptions` explicitly to require HTTP operation keys and current
receipt authorization/redaction; unconfigured handlers reject key headers.

Generic update/delete, reverse-cursor navigation, custom action metadata,
OpenAPI and browsable documentation are not implemented by this resource yet.
