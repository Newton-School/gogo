# Human-readable display filters

`core/contrib/humanize` provides `ordinal`, `apnumber`, `intcomma`, `intword`,
`naturalday`, and `naturaltime`. These presentation helpers never change models,
stored decimals, timestamps, or a request's locale. Admin and Async are not
dependencies. There is no process-global locale or filter registration.

```go
resolver, err := i18n.New(i18n.Config{
    Languages: []string{"en", "de"},
    TimeZones: []string{"UTC", "Europe/Berlin"},
})
if err != nil { return err }
display, err := humanize.New(humanize.Config{Resolver: resolver})
if err != nil { return err }
engine := templates.New(templates.Config{
    LocaleResolver: resolver,
    Filters: display.Filters(),
    Libraries: []string{"humanize"},
})
// Render with the request context containing the resolver's locale.
_ = engine
```

```html
{% load humanize %}
{{ rank|ordinal }}
{{ count|apnumber }}
{{ amount|intcomma }}
{{ amount|intcomma:False }}
{{ population|intword }}
{{ published_at|naturalday:"Y-m-d" }}
{{ published_at|naturaltime }}
```

The same operations are methods on `Formatter`, accepting `context.Context`.
Configure `Clock` for deterministic relative-time output. A formatter and its
returned filter maps can be shared across requests; do not mutate dependencies
or caller-owned input concurrently. Use the same `Resolver` for the template
engine, formatter, and optional translation catalog. Missing locale uses its
configured default; an incompatible inherited locale fails validation.

| Helper | Conversion and output |
| --- | --- |
| `Ordinal` | Nonnegative integers receive a contextual suffix, including 11–13 exceptions; negative integers remain numeric. |
| `APNumber` | Integers 1–9 use translated words; other integers remain numeric. |
| `IntComma(..., true)` | Exact decimal grouping, locale digits/separators, preserved fractional zeros, and locale grouping widths such as Indian 3/2. |
| `IntComma(..., false)` | Only the initial optional-minus/digit run is grouped with commas; all other text is retained, including exponent notation. Numeric-conversion failures in localized mode use this same literal fallback. |
| `IntWord` | Exact integer magnitude chooses million through decillion or googol; quotient rounds to one decimal, halfway away from zero. Plural selection uses the signed, unrounded quotient. The rounded display does not promote to the next unit. |
| `NaturalDay` | Compare displayed calendar dates: yesterday/today/tomorrow; otherwise a date-only format, default `DATE_FORMAT`. |
| `NaturalTime` | Sub-day elapsed seconds/minutes/hours; longer spans use calendar years/months and at most two adjacent nonzero units through minutes, with past/future wording. |

Numeric inputs include Go integer/float types, decimal strings, `json.Number`,
`big.Int`, and bounded pointer indirection. Integer helpers truncate numeric
fractions toward zero, but ordinary fractional/exponent strings remain text.
Binary floats retain their supplied value: integer helpers use its exact binary
integer conversion, while grouping uses Go's shortest round-trip decimal display.
Use decimal strings or `json.Number` for precision not representable in a float.
No `String`, `MarshalJSON`, arbitrary reflection methods, or arithmetic on stored
objects is invoked. Unsupported objects, booleans, nonfinite floats, invalid
UTF-8/NULs, excessive input, or malformed numeric `json.Number` in parsed mode
return a stable error. Nil values and nil supported pointers display empty.

Temporal methods accept `time.Time`; filters also accept `*time.Time`, nil, and
the engine's explicit-zone values. Other temporal inputs fail instead of calling
application conversion methods. Request zones, `{% timezone %}`, `{% localtime
off %}`, and `|utc`/`|timezone` compose without altering the caller's value.
Calendar-day comparison is separate from elapsed duration: a DST day can contain
23 or 25 hours. Long spans do not overflow Go's `time.Duration`. Time values must
have a displayed year in 1–9999 and zone offset strictly within ±24 hours.

`naturalday` fallback discards clock fields: `c` is `YYYY-MM-DD`, `I` is empty,
and time-only format tokens are rejected. Date tokens `r`/`U` require a midnight
anchor in the formatter's **project-default** zone, not the current request or
host zone. Nonexistent or ambiguous midnight fails explicitly. Named date
formats and month/day names currently use the existing deterministic English
template date formatter; this package does not introduce locale date catalogs.

## Translation and safety

Supply `Config.Translator` with an immutable `i18n.Translator` and catalogs in the
`humanize` domain. Plain source text is the fallback. Catalog keys are explicit
Gogo keys, not an automatically imported Django translation catalog:

| Family | Context | Message ID examples |
| --- | --- | --- |
| Small numbers and relative days | empty | `one`, `nine`, `yesterday`, `today`, `tomorrow`, `now` |
| Ordinals | `ordinal 0` through `ordinal 9`; `ordinal 11, 12, 13` | `{value}st`, `{value}nd`, `{value}rd`, `{value}th` |
| Large numbers | `intword` | `{value} million` (plural forms), likewise each unit |
| Sub-day relative time | `naturaltime-past` / `naturaltime-future` | `a second ago`, `a minute ago`, `an hour ago`; corresponding `from now` IDs, with plural forms containing `{value}` |
| Longer relative units | `naturaltime-past` / `naturaltime-future` | `{value} year`, `{value} month`, `{value} week`, `{value} day`, `{value} hour`, `{value} minute` with plural forms |
| Relative wrapper | `naturaltime-past` / `naturaltime-future` | `{value} ago`, `{value} from now` |
| Unit separator | `naturaltime-separator` | `, ` |

`{value}` is literal bounded substitution, never a template expression. Numeric
input is limited to 2,048 bytes/digits, exponent magnitude to 2,048 in parsed
mode, and translated substitutions to 64 KiB. Locale number patterns come from
bounded public `x/text` integer probes, not conversion of the user's large number
to a float. Unsupported pattern shapes fail rather than silently round.

Every result is ordinary text. The template engine applies contextual escaping;
even a translated ordinal containing HTML is not marked trusted. Explicitly
disabling template autoescaping remains the application's responsibility. Clock
panics return a stable unavailable error without leaking panic text, cancellation
returns no successful display, and formatting never writes external state.

The behavioral reference is [Django's humanize library](https://docs.djangoproject.com/en/6.0/ref/contrib/humanize/).
Gogo intentionally uses explicit typed/error and bounded-input contracts,
request-local configuration, exact large-number arithmetic, and plain-text
escaping. This is not a claim of complete Django localization or framework
parity. Calendar-only model field projection and other template numeric filters
are separate work.
