# Authorized private downloads

`NewDownloadHandler(service, DownloadOptions)` is an explicitly mounted GET/HEAD
attachment endpoint over an already configured [Service](service.md). It does
not register routes, validate uploads, expose storage URLs, sign public links,
or delete blobs. The service still owns current metadata, owner-link and object
read authority. Metadata UUIDs are not storage keys or filenames.

Supply both read-only callbacks: `Authorize(*http.Request) error` and
`ID(*http.Request) (string, error)`. Authenticate the request and check its current
request-level grant; select a canonical metadata UUID through a Gogo `urls.Param`
built-in UUID converter or a bounded URL value. Exact `auth.ErrUnauthenticated`
maps to 401, exact `auth.ErrPermissionDenied`/`files.ErrForbidden` to 403, and
exact `files.ErrInvalidOwner`/`files.ErrNotFound` to 404. Invalid UUIDs are 404;
opaque, mixed, panic and cancellation failures are safe 503. No provider message,
owner reference, object key or filesystem path is returned.

The handler snapshots its service and options at construction. Before context
methods or application callbacks, it validates at most 256 header names, 1024
values and 64 KiB of header text, plus bounded request scalars and 16 KiB of URL
components. Duplicate keys with different HTTP casing are rejected. Each
callback gets fresh header/URL data, with body, form caches, trailers, response,
TLS and other mutable transport pointers removed. It never reads or closes the
request body. Standard-library `Request.PathValue` match state is deliberately
not retained. Context values remain trusted opaque application services: Gogo
route params are available through that context, and custom converter values
must be treated as trusted, read-only values, not mutated by the callbacks.

Request authorization runs before ID extraction and once more immediately before
`Service.Open`. Every selected GET/HEAD, including a prospective 304, 412 or 416,
then performs the service's owned read-only transaction, ready-state/current
owner-link checks and object grants before storage access. That snapshot is the
authorization observation point; replacement or ACL revocation can deny later
opens but cannot recall an already-started stream. Independently mutable ACLs
require application coordination. Read-only hooks must not write database or
authorization state.

## Representation and requests

The default fixed attachment filename is `download`. Configure a UTF-8 name of
at most 255 bytes, without controls, path separators or dot/space-only names.
There is no inline or dynamic filename mode. The canonical prevalidated stored
MIME type is accompanied by `nosniff` and `Cache-Control: private, no-store`.
Attachment names are formatted as parameters, including internationalized
names; they are not interpreted as storage paths. See [RFC 6266](https://www.rfc-editor.org/rfc/rfc6266.html).

The strong ETag is a stable opaque hash of the selected identity and immutable
representation metadata, including the configured disposition. Last-Modified
uses finalization time, clamped to the captured response Date if it is in the
future; unrepresentable HTTP dates are omitted. Only a non-clamped finalization
date for the immutable file identity is a strong date validator. These choices
follow [RFC 9110 validators](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.8).

Conditional tag fields are limited to 32 lines, 4096 bytes and 64 members each;
dates, If-Range and Range are singleton fields of at most 128 bytes. Excessive
shape is 400 before storage. A failed/malformed If-Match is 412 and its presence
suppresses If-Unmodified-Since. If-None-Match uses weak comparison; its presence
suppresses If-Modified-Since even for an invalid cache hint. Matching validators
return 304 only after authorization. Invalid date hints are ignored. If-Range
requires an exact strong ETag or an exact strong date, otherwise the full
representation is selected. See [RFC 9110 conditionals](https://www.rfc-editor.org/rfc/rfc9110.html#section-13).

GET supports one `bytes` interval: `start-end`, `start-`, or `-suffix`, with
case-insensitive unit names and checked integer arithmetic. Unknown units and
all multiple-range requests are ignored (full 200); malformed or unsatisfiable
single byte ranges return 416. Selected intervals return 206 with exact framing.
HEAD ignores Range, advertises the full size, and never calls `Reader.Read`.
No multipart range worker or buffering is introduced. See [RFC 9110 ranges](https://www.rfc-editor.org/rfc/rfc9110.html#section-14).

## Streaming and failure ownership

Configure the existing `core/http.ServerConfig.Streaming` selector for the route
as shown in the compiling example. A selector is not an authorization check.
Do not apply byte-changing compression or buffering timeout middleware: the
advertised strong validator, lengths and offsets describe the original bytes.

The handler checks the opened reader's exact seek size, selects/seeks an interval,
and pre-reads the first bounded chunk before committing headers. No-body outcomes
close the reader before headers. It streams with at most 64 KiB of transfer
buffer, checking exact writes and cooperative cancellation between operations.
The timeout defaults to 30 seconds (valid 1 ms through 5 minutes); it cannot
force an opaque blocking callback, Reader or ResponseWriter to return.

Reader ownership is released exactly once. A read/seek/close failure before
headers returns safe 503. Once headers have begun, any failed/short transfer,
panic, cancellation or close failure aborts through `http.ErrAbortHandler`,
never appending an error document to private bytes. A client may already have a
prefix, or even all advertised bytes when a later close fails; an abort cannot
retract those bytes. This endpoint does not claim per-read checksum verification
or retroactive revocation, and it relies on the storage provider's immutable
blob and `io.Reader`/`io.Seeker` ownership contracts.
