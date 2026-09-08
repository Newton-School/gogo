# Scoped day archives

`NewDayArchiveView` composes the existing model read boundary and named templates with one explicit calendar day. It registers no route, creates no table, and performs no query at construction. The external-package example shows backend injection and named routing; migrations and authentication middleware remain application-owned.

## Options and calendar selection

`DayArchiveViewOptions` embeds `ListViewOptions`, with these additions:

| Option | Contract |
| --- | --- |
| `DateField` | Required intrinsic local stored Date or DateTime field; nullable values are excluded by date predicates. No relation, custom codec or generated-field expansion. |
| `Date` | Required read-only callback returning exactly ASCII `YYYY-MM-DD`, years 1–9999. Exactly `ErrInvalidLookup` means malformed input; other callback errors are operational. |
| `AllowEmpty` | Defaults false: an empty archive is 404 and navigation seeks occupied periods. True permits an empty first page and uses adjacent calendar periods. An empty nonzero-offset page is still 404. |
| `AllowFuture` | Defaults false: Date rows must be at or before the selected locale's today; DateTime rows must be at or before the captured current instant. True removes this cutoff. |
| `Clock` | Defaults to `time.Now`. Called once when future rows are excluded; its returned instant is validated and detached. Not called for OPTIONS or when `AllowFuture` is true. |

The template locale resolver is captured privately and drives both selection and rendering. Without one, the inherited locale or UTC applies, never the process's local timezone. Scope runs before the date decoder, clock and pagination parsing. Date uses calendar equality; DateTime uses separately resolved local midnights in a half-open interval, not a fixed 24-hour duration. Invalid input or an ambiguous, nonexistent or unrepresentable requested interval returns 404.

Unlike `NewDateDetailView`, an archive excludes **later instants today** by default. A future requested day with `AllowEmpty: true` may return an empty 200, because the publication cutoff empties its query; it is not rejected merely for being a future day.

## Projection, pagination and navigation

`Fields`, `PolicyFields`, authentication, model/object policies and template safety retain the existing scoped model-view contract. Nil `Ordering` means descending DateField followed by primary-key ties; an explicit empty slice means primary-key ordering only. Explicit ordering uses the existing scalar allowlist. Pagination supports page-number and limit/offset modes with the same size/offset limits, strict query validation and relative links as `NewListView`. Unknown/repeated/mixed pagination parameters return 400.

Reserved context wins over application context:

| Name | Value |
| --- | --- |
| `object_list` | Explicitly projected object maps; date fields and primary keys are never automatically exposed. |
| `page` | Existing size, offset, next/previous-link and boolean navigation metadata. |
| `day` | The requested calendar date as a canonical string. |
| `previous_day`, `next_day` | Canonical neighboring date strings, or nil. |
| `previous_month`, `next_month` | Canonical first-of-month strings, or nil. |
| `date_list` | Nil, matching the day archive's lack of a date-group listing. |

With `AllowEmpty: false`, each direction makes at most one limit-one query outside the current day/month, retaining the frozen scope and publication cutoff. This requires no count, date grouping or unbounded scan in Go. Navigation privately reads the contributing date plus declared policy fields/identity; its policy records still expose only the original selected fields. The date does not become a new object field.

Occupied navigation has an explicit **DayArchive-only** metadata grant rule: the contributing object's `view` permission and `AllowField(DateField)` must both allow disclosure, even if DateField is not in `Fields`. If the date was not selected for policy use, it remains deferred in the callback record. Exact object denial or a false field grant omits that direction; the view never searches past it for a later candidate. Other failures abort the entire response. Included contributors and their date grants are rechecked after rendering; revocation aborts rather than emitting stale metadata.

With `AllowEmpty: true`, neighboring calendar values are derived from the requested date and need no invented object or field grant. Future candidates are omitted unless future dates are allowed. In both modes unsupported target intervals are omitted. Unsupported boundaries needed solely for a neighbor also omit that direction, without turning an otherwise valid requested day into 404.

## Bounds and outcomes

The page reads at most page-size-plus-one rows; navigation adds at most four rows. All five reads share the existing 8 MiB value/text and 65,536-node materialization budget, including policy-only data and JSON parsing work. Reader completion errors invalidate the complete result, including already collected rows or dates. The named template has its ordinary context/output limits.

GET and HEAD use the same queries, rendering and grants; HEAD emits no body. Opt-in OPTIONS checks request/model permission without date hooks or database reads. Unsafe methods return 405. Responses are private, no-store and nosniff. Missing/invalid/empty requested archives return 404; request/model or displayed-object denial follows the existing 401/403 rules; provider, completion, callback, cancellation and render failures return safe 503 without partial HTML.

Callbacks must be read-only and cooperative. This view starts no transaction, does not lock an independent ACL service, and promises neither a cross-query database snapshot nor stable pagination across concurrent writes; caller-owned ORM transaction behavior remains intact. The application must encode all navigation visibility in Scope. This slice adds only DayArchive, not Today/Week/Month/Year/ArchiveIndex, editing views, date-format route inference or automatic template naming.
