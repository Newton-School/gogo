# Request and task locales

`core/i18n` provides immutable language/time-zone selection. Construct one
resolver at startup; put its middleware **after authentication** when using the
trusted `UserPreferences` profile reader.

```go
locales, err := i18n.New(i18n.Config{
    Languages:       []string{"en", "fr", "hi"},
    DefaultLanguage: "en",
    TimeZones:       []string{"UTC", "Asia/Kolkata", "America/New_York"},
    DefaultTimeZone: "UTC",
})
if err != nil {
    return err // Invalid configuration is a startup failure.
}
handler := locales.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    locale, _ := i18n.FromContext(r.Context())
    fmt.Fprintln(w, locale.LocalTime(time.Now().UTC()).Format(time.RFC3339))
}))
```

| Priority | Language | Time zone |
| --- | --- | --- |
| 1 | Explicit `WithLocale` context, validated against this resolver | Same override; unspecified values inherit the context or project default |
| 2 | Current trusted profile preference, if configured | Supported profile IANA zone |
| 3 | Exactly one supported `gogo_language` cookie (name configurable, can be disabled) | No cookie selector |
| 4 | Bounded `Accept-Language` weighted matching against configured languages | Never inferred from language or arbitrary request headers |
| 5 | Configured default (first language unless explicitly selected) | Configured default, UTC if omitted |

Profile/cookie values are canonicalized but must name configured tags exactly.
Obsolete or malformed preferences fall through; a profile reader error, panic or
request cancellation does not. Explicit unsupported overrides return
`ErrInvalidLocale`. Headers can match supported language/script variants; client
extensions cannot add a language or calendar outside the configured selection.
Header matching uses Go's [language matcher](https://pkg.go.dev/golang.org/x/text/language).
It is advisory preference selection, not a strict HTTP acceptability gate: a
missing, malformed, oversized or unmatched header selects the project default,
including an all-zero-weight header, rather than returning 406.

The allowlists are copied at startup (maximum 256 entries each). Time zones are
loaded once with embedded IANA data; the host-dependent `Local` zone is rejected.
Accept-Language is limited to 4096 bytes, 16 header lines and 32 ranges. Cookies
are limited to 16 KiB across 32 lines; duplicate language cookies are ignored.
These are selector bounds, not a substitute for the HTTP server's request limits.

Middleware attaches the selected context and sets `Content-Language` and `Vary:
Accept-Language`, adding Cookie when applicable. A profile reader or explicit
context additionally sets `Cache-Control: private, no-store`; profile readers
also add Authorization to Vary. Downstream handlers and cache middleware must
preserve these dimensions and restrictions. Preference failures stop before the
application handler with a stable 503 response (504 for deadline expiration),
without provider details. No language cookie is written automatically.

## Background work and ORM integration

Contexts are process-local and are **not** serialized by Async. Include only the
plain `locale.Preferences()` data in an application-owned task payload, then
validate it at execution time with the worker's resolver:

```go
ctx, err := locales.WithLocale(ctx, payload.Preferences)
if err != nil {
    return err
}
locale, _ := i18n.FromContext(ctx)
displayTime := locale.LocalTime(storedUTCInstant)
```

Applications can connect `orm.Store.Timezone` to `FromContext(ctx).Location()`
with UTC as their explicit fallback. No global timezone, language or stored
instant is modified; distinct UTC instants in a daylight-saving fold stay
distinct. Returned location pointers and local-time values do not expose the
resolver's shared location pointers. This package does not parse
ambiguous/nonexistent local wall times into an arbitrary instant:
`Locale.ResolveLocal` explicitly rejects them. See the
[form datetime contract](../forms/datetime.md) for strict wall-time input and
request-local widget round trips.

For immutable application catalogs, plural/context lookup and explicit lazy
messages, see [translation catalogs](catalogs.md). Localized template/form
formatting, locale URL prefixes, language-switch endpoints and automatic task
propagation are not implemented by the locale-selection middleware.
