# Date/time form round trips

`DateTime` and `SplitDateTime` use the locale carried by `forms.WithContext(ctx)`;
`ModelForm` forwards its construction context automatically. Without an attached
locale, naive input uses UTC, never the host's `time.Local`.

| Input or display | Behavior |
| --- | --- |
| Timezone-less datetime | Interpret its wall-clock components in the current locale. Exactly one corresponding instant must exist. |
| Numeric-offset/RFC3339 input | Preserve the explicit instant and normalize the cleaned value to UTC; a numeric offset disambiguates a repeated wall time. |
| Trusted initial `time.Time` | Preserve its existing instant, including either side of a fold; disabled fields ignore forged submitted values. Split fields still run component validation. |
| Initial datetime widget | Convert the instant to the current locale before rendering; preserve fractional seconds. DateTime/SplitDateTime default output is accepted by default input parsing. |
| Date-only / time-only | Calendar data, not instants; no timezone shift. |
| Invalid bound value | Keep the submitted safe value for redisplay, with field errors and accessible invalid-state markup. No model save is produced. |

Default DateTime inputs include RFC3339, `YYYY-MM-DDTHH:mm[:ss[.fraction]]` and the
equivalent space-separated forms. Explicit `InputFormats` use Go layouts, with
64 layouts/256 bytes per layout and 4096-byte input bounds. Numeric-offset layouts
are supported; named abbreviations such as `MST` are refused because they depend
on host/season and Go may fabricate unknown abbreviation zones. Custom composite
`Compress` callbacks remain application-owned transformations.

Daylight-saving gaps and folds report `ambiguous_timezone`; neither is silently
normalized. This follows [Django's timezone-aware form input behavior](https://docs.djangoproject.com/en/6.0/topics/i18n/timezones/#time-zone-aware-input-in-forms).
A datetime-local HTML control cannot encode an offset. To choose a particular
instant during a fold, use a trusted custom widget that submits numeric-offset
input; the framework does not guess the earlier/later occurrence.

`i18n.Locale.ResolveLocal(ctx, i18n.LocalDateTime{...})` exposes the same strict
wall-time conversion outside forms. It walks actual Go timezone transition
boundaries, rather than sampling offsets hourly. Supported offsets are within
±24 hours, with at most 64 periods examined; years and the resulting UTC instant
must lie within 1–9999. Lord Howe half-hour changes, historical whole-day gaps and
23-hour folds are covered by tests. Exotic unsupported timezone data fails.

The public widget interface remains context-free: direct `Widget.Render` calls
format the supplied value. `Form.BoundField` performs request-local conversion
before handing the value to a widget. This checkpoint does not add localized
month names, locale-specific date layouts, currency/number formatting or template
timezone tags. PostgreSQL stores timestamp precision at microseconds; form
parsing/rendering preserves nanoseconds until the database applies its precision.
