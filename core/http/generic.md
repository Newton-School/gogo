# Read-only generic views

`NewTemplateView(TemplateViewOptions)` and `NewRedirectView(RedirectViewOptions)`
return `(net/http.Handler, error)`. Register the resulting handler with
`core/urls` or an ordinary Go HTTP router. Construction failure returns
`ErrGenericConfiguration`; no server, database, worker or environment is started.
Executable public-package examples are in `generic_example_test.go`:

```sh
go test ./core/http -run '^ExampleNew(Template|Redirect)View'
```

These are the Template and Redirect slices only. List, Detail, form/edit and date
archive views are not supplied by these constructors; this is not a claim of
complete Django generic-view parity.

## Shared access and request boundary

Both options embed `ReadViewOptions`. `Authorize func(*http.Request) error` is
mandatory, even for a deliberately public page. Return `nil` only when access is
allowed; private applications must inspect the verified principal and enforce
their own resource/tenant scope. Cookie authentication and token restrictions
remain in the inherited request context; the view does not authenticate claims
from a query or header for you.

The handler validates the method and rejects request bodies before callbacks.
GET and HEAD are supported. `AllowOptions: true` also permits OPTIONS: it checks
access twice and returns 200 with `Allow`, without running Context, Target,
Reverse, template processors or external-target checks. Otherwise unsupported
methods return 405 with `Allow`. When using `core/urls`, omit route-level method
filters to let this handler own method dispatch; router-level method handling
can short-circuit before any view runs.

GET/HEAD execute: initial access check → read/context/target → complete rendering
or redirect validation → final access check → write. HEAD performs the same
work and emits the GET content length without a body. Hooks must not hide saves,
deletes, counters, job publication or other business writes inside this path.

| Outcome | Response |
| --- | --- |
| Template success | 200 HTML |
| Redirect success | 302, or 301 with `Permanent: true` |
| Intentionally empty redirect destination | 410 |
| Malformed/body-bearing request | 400 |
| Exact `auth.ErrUnauthenticated`, `auth.ErrPermissionDenied`, `http.ErrNotFound` from access, Context or redirect hooks | 401, 403, 404 respectively |
| Wrapped/joined errors, provider/render failures, panic, cancellation or timeout | 503, no partial page or Location |

Processor/loader/render failures are operational failures, not access decisions.
All responses use `Cache-Control: private, no-store` and
`X-Content-Type-Options: nosniff`; callback details are not disclosed. This slice
does not add conditional caching or ETags. A failed write aborts the transfer.

`Timeout` defaults to five seconds; an explicit value must be between one
millisecond and one minute. It bounds cooperative callbacks through the request
context. It does not kill a goroutine or sandbox application code. A callback
that ignores cancellation may continue blocking its request.

Options and request metadata are snapshotted. Every request hook gets a fresh
copy of URL, headers and Go `PathValue` storage; replacing those fields cannot
change subsequent callbacks, query forwarding or HEAD emission. Pre-parsed form
caches and the body are discarded; each hook can parse its own frozen query.
Inherited context services, custom loader state and TLS connection objects
remain trusted application-owned values, not deeply cloned sandboxes. Those
objects retain their own authority and concurrency contracts.

Captured route parameters are limited to 64 immutable scalar values: strings,
booleans, integers and finite floats, including named scalar types, with a
combined 16 KiB key/string budget. Opaque or mutable custom-converter values
are rejected before access callbacks. This restriction belongs to these generic
views, not to the underlying router's general converter API.

## TemplateView

Provide a fixed `TemplateName` and `templates.Config` with an application-owned
loader. There is no embedded fallback template. Valid names are bounded to 255
bytes and may address a named partial such as `page.html#summary`. Template
loading/rendering happens only after the initial access check. A missing source
fails closed instead of interpreting user content as a template.

`Context func(*http.Request) (templates.Context, error)` is the read-only hook for
selected page data; `ExtraContext` supplies fixed values. Effective precedence,
lowest first, is:

```text
processors (later processor wins) → captured route parameters
→ ExtraContext → Context
```

Processors execute with the template engine's initialized locale context.
Their results and explicit values are detached before further rendering hooks;
the final merged context shares one budget. No request, model, principal or
database is automatically exposed as a template variable.

| Limit | Bound |
| --- | --- |
| Context traversal depth | 32 |
| Context work, including keys and visited values | 65,536 |
| Context key/string bytes, including the final merged context | 8 MiB |
| Rendered output | 8 MiB default; `MaxOutputBytes` may lower it or raise it to at most 16 MiB |
| Configured loaders / processors | 32 each |

Data is projected into detached scalar values and collections; exported struct
fields are supported, private fields and receiver methods are not exposed.
Unsupported values, cyclic/deep structures and exceeded budgets fail closed.
`ExtraContext` and built-in `MapLoader` contents are copied at construction;
custom loaders and extensions remain trusted application code.

Ordinary strings are contextually escaped. Text such as
`<em>{{ title }}</em>` stays data: it does not execute `{{ title }}`.
`templates.SafeHTML` and `html/template.HTML` assert that trusted server code has
already produced safe HTML; they do not sanitize untrusted content. Templates,
filters, tags, includes, processors and loaders are not an untrusted-code sandbox.

## RedirectView

Choose at most one source:

- `URL`: a literal destination, with no Python `%` interpolation or template
  execution. A literal URI escape such as `%25` stays an escape.
- `PatternName` plus `Reverse`: use `Router.Reverse`'s signature and frozen route
  parameters, including namespaces such as `articles:detail`. The reverse
  callback receives no forwarded query; forwarding occurs after reversal.
- `Target func(*http.Request) (string, error)`: resolve authorized read-only data
  explicitly. An empty successful result means 410; an error never means Gone.

No source also means 410. An empty successful named reverse is invalid and
returns 503. Ordinary access checks still apply to Gone responses.

`QueryString` defaults to false. When enabled, the original nonempty raw query
is appended before the destination fragment, using `&` if the destination
already has a nonempty query. Order, duplicate members and escape spelling are
preserved. For example, `/new?own=1#part` plus `x=2&x=3` becomes
`/new?own=1&x=2&x=3#part`. Explicit empty `?` and `#` delimiters are retained.

Targets have a 2048-byte raw and final ASCII-encoded bound, including forwarded
query and fragment. Only rooted relative paths and absolute HTTP(S) URLs are
accepted. Unicode path/query/fragment data is escaped; authority names must
already be ASCII DNS/A-labels or valid IP addresses. Case, one DNS trailing dot
and default ports normalize. Userinfo, network-path references, controls,
backslashes, invalid escapes, ambiguous numeric hosts, dot segments, encoded
path separators and nested structural path encodings are refused. Query data
may contain encoded URLs, but syntax validation does not establish its privacy.

Every absolute destination requires `AuthorizeExternal`, even if its host
matches the request. A fixed absolute URL without this policy is a construction
error; a dynamic absolute target without it is denied. The policy receives the
complete normalized Location, including any forwarded query, after resolution
and again after the final ordinary access check. No authority is inferred from
Host/Forwarded headers. The target is never fetched, followed or written by the
framework; policies must explicitly decide what information may be disclosed
to that destination.
