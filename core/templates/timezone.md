# Request-local template timezones

`Config.LocaleResolver` uses the same immutable `core/i18n.Resolver` as locale
middleware and background tasks. Its default applies when no locale was supplied;
an inherited locale must belong to its allowlists. Without a resolver, an
inherited locale or UTC applies, and named template overrides are unavailable.
Rendering never changes `time.Local`, a caller's context or stored instants.

```go
locales, err := i18n.New(i18n.Config{
    Languages: []string{"en", "fr"},
    TimeZones: []string{"UTC", "Europe/Paris", "America/New_York"},
})
if err != nil { return err }
engine := templates.New(templates.Config{
    LocaleResolver: locales,
    Loaders: []templates.Loader{templates.FSLoader{FS: pages}},
})
// Use the request context selected by locales.Middleware, or an explicit task
// context produced by locales.WithLocale. Pass time.Time values, not strings.
output, err := engine.Render(ctx, "receipt.html", templates.Context{"created": created})
```

| Template expression | Behavior |
| --- | --- |
| `{% load tz %}` | Recognized built-in library; no runtime disk/plugin loading. |
| `{{ created\|date:"Y-m-d H:i O" }}` | Format in the current request/block timezone. |
| `{% localtime off %}…{% endlocaltime %}` | Preserve the supplied instant's location within the block. `on` or an omitted argument enables conversion. |
| `{% timezone "Europe/Paris" %}…{% endtimezone %}` | Override only this nested block, using a configured allowed zone. |
| `{% timezone None %}…{% endtimezone %}` | Use the configured project default within the block. |
| `{{ created\|localtime }}` | Explicitly convert even inside `localtime off`. |
| `{{ created\|utc }}` | Explicit UTC output; later date filters do not undo it. |
| `{{ created\|timezone:"Europe/Paris" }}` | Explicit allowed-zone output; no arbitrary zone-file lookup. |
| `{% get_current_timezone as zone %}` | Assign the current IANA timezone name. |
| `{% now "H:i O" as clock %}` | Capture current UTC time and use the current timezone, including inside `localtime off`. |

Includes, inheritance, `with`, loops and partials retain the current block's
timezone. Nested blocks restore prior state on success or error. Explicit filter
results retain their conversion intent when stored in a `with` variable; comparing
them still compares instants, not location-pointer identity. Date formats and
`now` formats are limited to 4,096 bytes. Invalid overrides, malformed tags and
foreign resolver contexts fail without returning partial HTML.

All Go `time.Time` values represent instants, including pointer fields. There is
no implicit naive-string parsing in templates; use forms or `Locale.ResolveLocal`
to validate wall times before rendering. Both sides of a daylight-saving overlap
therefore preserve their actual offset. Direct time output retains Go's textual
representation; use an explicit date/time filter for the desired presentation.
The current date-format implementation is still partial, including localized
format names and translated month/day names; this checkpoint is not complete
Django date-format parity.

Custom filters should use `templates.TimeValue(ctx, value)` to read either a Go
instant or an explicitly converted template value with the current display rules.
It returns a detached `time.Time`. Returning an ordinary `time.Time` from a custom
filter allows normal local conversion on later output. No value is automatically
marked as trusted HTML, and automatic HTML/attribute/JavaScript escaping remains
active. `autoescape off` remains an explicit trusted-template capability.

The behavior follows the block and explicit-conversion model documented in
[Django's timezone template API](https://docs.djangoproject.com/en/5.2/topics/i18n/timezones/#time-zone-aware-output-in-templates),
with Gogo's explicit project allowlist and Go-native instant types.

Verification covers nested restoration, includes/inheritance, pointer values,
explicit conversion persistence, DST overlaps, denial/cancellation, source
location isolation, contextual escaping and concurrent shared-engine rendering.
The `FuzzTemplateTimezone` target exercises malformed nested syntax and verifies
that each subsequent render retains its configured default.
