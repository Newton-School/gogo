# Scoped dated detail views

`NewDateDetailView` composes the ordinary `DetailViewOptions` with an explicit stored `DateField`, a required `Date` decoder, `AllowFuture`, and an optional `Clock`. It uses the same mandatory request/model/object grants, row scope, bounded materialization, field allowlist, escaped template output and final permission checks as `NewDetailView`.

`Date` returns an exact ASCII `YYYY-MM-DD`, with a valid Gregorian year from 1 through 9999. It can assemble the value from typed router parameters; the framework does not guess parameter names, month formats or routes. Return exactly `ErrInvalidLookup` for malformed input. The required `Key` decoder still returns every primary-key component, without extras. Root scope is evaluated and frozen before either decoder. Date validation precedes identity decoding; both receive independent request metadata.

| Setting | Behavior |
| --- | --- |
| `DateField` | Local stored intrinsic `Date` or `DateTime`; relations, custom codecs and other kinds are rejected during construction. Nullable fields are allowed; SQL null cannot match a day. |
| Date field | Exact calendar-date equality, without timezone conversion. |
| DateTime field | Half-open UTC interval between separately resolved local midnights. A daylight-saving day may be 23 or 25 hours. |
| `Templates.LocaleResolver` | One privately captured resolver value selects both query and template locale. An allowed inherited selection takes precedence over its default. Without a resolver, the inherited locale or UTC is used. No host-local timezone fallback. |
| `AllowFuture` | Defaults to false: requested calendar days after today in the selected locale return 404 before identity lookup or SQL. Later instants on today remain eligible; this option is not a publication-time authorization rule. |
| `Clock` | Defaults to `time.Now`; called once for a valid read when future days are disabled. Not called for `AllowFuture: true`, OPTIONS, or invalid dates. Only its instant determines today. |

Keep publication/tenant/ownership restrictions in `Scope`. The date filter only narrows that scope. Filtering on a field does not load it into policy records or disclose it in `object`; include it explicitly in `Fields` or `PolicyFields` when needed. Reserved context remains only `object`, with the same precedence and bounds as ordinary detail. Query parameters have no additional built-in meaning.

## Outcomes

Malformed dates or keys, future days, missing/scoped-out records and revoked detail-object/visible-field grants return 404. Ambiguous or nonexistent local midnights also return 404 rather than guessing a DST transition. Both UTC endpoints must fit years 1..9999: in particular a DateTime lookup on 9999-12-31 cannot form its next-midnight endpoint, whereas a Date equality can represent that day.

Invalid inherited locale, scope/provider/row-completion failures, non-input decoder errors, invalid clock instants, cancellation, callback panics and template failures return safe 503 responses with no partial object HTML. Invalid construction returns `ErrGenericConfiguration`. Error details do not disclose callback or database values.

GET and HEAD perform the same reads and grants; HEAD has no body. Opt-in OPTIONS performs only request/model grants. Unsafe methods return 405. Responses are private, no-store and nosniff; no save/delete, migrations, routes, cache or implicit transaction is created. The same read-only callback and ambient-transaction contracts as ordinary generic model views apply.

This is the dated-detail slice, not archive lists, date navigation, implicit template suffixes, automatic slug lookup or editing views. The day-based future rule and DateTime interval match Django's dated-detail structure; explicit project-local today and rejection of ambiguous/nonexistent boundaries are Gogo's timezone contract, not a claim of identical behavior for every Django timezone edge.
