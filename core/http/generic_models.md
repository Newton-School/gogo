# Scoped HTML model views

`NewListView` and `NewDetailView` compose an existing `orm.Store`, explicit model permissions and row scope, and a named template. Register the model schema and apply its migrations before serving requests. The constructors register no routes, create no tables, and execute no queries.

See the compiling external-package examples in `generic_model_example_test.go` for a backend-injected store, model registry, scoped policy, and URL routing.

## Data and access boundaries

| Option | Contract |
| --- | --- |
| `ReadViewOptions.Authorize` | Required request grant, including deliberately public pages; checked before reading and before emission. |
| `Store`, `Model` | Configured backend and registry; exact registered `app.Model` key. |
| `Policy` | Token-constrained `view` grant on the model and each displayed object; object grants are repeated after rendering. |
| `Scope` | Required root predicate before key/pagination parsing or SQL. It must include all row visibility affecting list membership and navigation; there is no unscoped fallback. |
| `Fields` | Required local stored-field output allowlist. No all-fields default. |
| `PolicyFields` | Additional loaded fields for object/field decisions; never automatically included in template rows. |
| Primary keys | Loaded for identity, but only included in template rows when explicitly listed in `Fields`. Composite keys require every component. |
| `AllowField` | Optional read-only narrowing of `Fields`. Each callback receives a detached selected-field record; unselected fields remain deferred. Previously omitted fields cannot appear during final checks. |

Authentication comes from trusted middleware, not request claims. Anonymous access requires `AllowAnonymous: true` **and** a policy that explicitly allows it. Authenticated inactive or empty-ID principals are denied. A custom policy cannot widen a token's permission ceiling.

The view copies configuration and freezes selected values before template callbacks. Policies receive fresh record and identity snapshots; templates receive plain projected data, not live model objects, the store, or an automatically exposed principal/request. Template sources and extensions remain trusted application code. Reserved model values override processors, route parameters, extra context and dynamic context.

## Template values and pagination

| View | Reserved context |
| --- | --- |
| List | `object_list`: projected row maps; `page`: `size`, `offset`, `has_next`, `has_previous`, `next`, `previous`. |
| Detail | `object`: one projected row map. |

Lists support `pagination.PageNumber` (`page`, `page_size`) or `pagination.LimitOffset` (`limit`, `offset`). Defaults are 50 rows, maximum 200, and maximum offset 1,000,000; configuration can narrow page size or change the offset limit. Unknown, repeated, malformed or mixed-mode parameters return 400. Links are relative and preserve only these validated parameters.

Nil `Ordering` uses model ordering; an explicit empty slice uses primary-key ordering. Configured ordering must use supported local scalar fields, with primary keys appended as deterministic ties. Detail lookup clears list ordering. Lists read at most page-size-plus-one rows and display only the page; `has_next` is true only when a usable next link fits the offset limit. Empty lists return 200. Pagination may drift during concurrent writes; no multi-request snapshot or total count is promised.

`DetailViewOptions.Key` returns exactly every primary-key field, with no extras or nil values. Intrinsic parsers validate lookup values without executing save validators. Return exactly `ErrInvalidLookup` for malformed input, not wrapped provider errors. Lookup is always combined with root scope; two matching rows are an error, not an arbitrary first result.

## Initial field support and bounds

Supported local fields are integer/auto variants, UUID, decimal, finite float, boolean, text/character/slug/email/URL/IP/file-path strings, date, time, datetime, duration, and bounded JSON. Decimal precision remains textual. Date and time render as calendar/clock strings; datetime remains a detached UTC instant for template timezone formatting. JSON numbers retain their exact lexical spelling as template strings; JSON null projects to nil. String values never become trusted HTML automatically.

Selected custom codecs, foreign keys/one-to-one/many-to-many fields, generated fields, binary/file/image fields, and backend extension fields are explicitly refused in this initial boundary. Related model expansion, computed fields, implicit file URLs and display-method execution are not included. Unsupported descriptors fail construction rather than being stringified or silently skipped.

There are at most 64 output fields, 64 policy fields, 64 key components and 1,024 schema fields. Materialization enforces the row cap independently of SQL, with shared bounded value/string accounting including policy-only data. Generic template context uses depth 32, 65,536 visited values/keys and 8 MiB of string data; template output defaults to 8 MiB and can be configured up to 16 MiB. Terminal reader errors invalidate the entire HTML result, including an already decoded prefix.

## Outcomes and remaining scope

| Outcome | HTTP result |
| --- | --- |
| Unauthenticated private request | 401. |
| Model or list-object denial | 403. |
| Malformed list pagination | 400, after the initial scope decision and before SQL. |
| Missing scoped detail, malformed detail identity or exact detail-object denial | 404. |
| Duplicate detail, failed scope/provider/decoder/row completion, callback panic or rendering failure | 503 with a safe body, never partial model HTML. |
| Invalid constructor configuration | `ErrGenericConfiguration`; no handler returned. |

GET and HEAD perform the same reads, rendering and grants; HEAD omits the body. Opt-in OPTIONS performs request/model authorization without scope queries, key decoding or rendering. Unsafe methods return 405. Responses are private, no-store and nosniff.

Hooks must be read-only and cooperative with context cancellation; the framework performs no save/delete or implicit write hooks. Go callbacks are trusted code, not a sandbox. Final grants do not lock an independent ACL database: applications own coordination for concurrently changing external authority. The view does not start a transaction or claim a repeatable-read snapshot; ordinary ORM ambient-transaction behavior applies.

This slice includes no total counts, cursor pagination, client-defined filters/order, eager relation output, editing views, date archives, automatic Admin registration or caching. Those remain separate framework capabilities, not implicit behavior of these constructors.
