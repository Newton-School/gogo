# Pagination and rate limits

## Parse a bounded page

This executable example needs no database. Run `go test ./docs/examples -run Example_pagination -v` from the framework checkout:

**Set page limits**

{{snippet docs/examples/pagination_test.go pagination-config}}

**Parse a page request**

{{snippet docs/examples/pagination_test.go pagination-parse}}

Result: offset `10`, size `10`. Apply authorization scope before counting or paging.

<details>
<summary>Complete runnable example, including imports</summary>

{{code docs/examples/pagination_test.go}}

</details>

In a handler, replace the literal query values with `r.URL.Query()`. After parsing, apply the caller's visibility predicate to the ORM query, then its offset and limit. A pagination helper is not a row-authorization layer.

Bound every collection endpoint. Pagination controls work and response size; authorization determines which records can enter the collection at all.

## Pagination modes

| Mode | Parameters | Behavior |
| --- | --- | --- |
| Page number | `page`, `page_size` | Conventional numbered pages |
| Limit/offset | `limit`, `offset` | Explicit slice of an ordered result |
| Forward cursor | `cursor`, `page_size` | Signed position in an explicitly stable ordering |

API resources default to page size 50 with maximum 200. Mixed, duplicate, invalid or undeclared parameters fail validation. Ordering includes all primary-key components as tie-breakers. Counts and page results are separate reads, not a snapshot transaction.

## Cursor requirements

Configure a purpose-bound signer, resource name/version, immutable ordering fields and a current scope identity. Cursor keys must be non-null scalar values, immutable across all write paths and directly represented by the serializer.

The cursor binds the selected scope, principal/permission ceiling, filters, ordering and page size. It is signed, **not encrypted**. Do not put secret values in ordering keys. A stale, forged or rebound cursor is rejected; supported navigation is forward-only.

## Rate limiting

`core/ratelimit` defines the limiter contract; the Redis connector supplies a distributed implementation. Choose a key based on trusted identity and the specific operation. A caller-supplied header or untrusted forwarded IP must not be accepted as an authority boundary.

Use bounded limits/windows and decide deliberately how provider outages affect your endpoint. A process-local limiter cannot enforce a shared limit across pods. Rate limiting reduces traffic; it does not replace authentication or permission checks.
