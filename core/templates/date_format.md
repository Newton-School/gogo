# Date and time presentation

`date`, `time` and `now` share one bounded formatter. The timezone library first
selects the display instant; formatting never writes to a model or changes
global locale state. See [timezone behavior](timezone.md).

| Family | Format tokens |
| --- | --- |
| Day and week | `d j D l S w z W` |
| Month and year | `m n M b E F N t y Y L o` |
| Clock | `g G h H i s u a A f P` |
| Zone and standard forms | `e I O T Z c r U` |

These follow [Django's date-format character meanings](https://docs.djangoproject.com/en/5.2/ref/templates/builtins/#date).
Names, AM/PM labels and named format defaults currently use deterministic English
fallbacks, even when the request language is different. `E` has the same English
month name as `F`. Locale-specific date names, inflections and format preferences
remain a separate integration requirement.

```django
{{ created|date:"Y-m-d H:i O" }}
{{ created|date:"jS F Y" }}
{{ created|time:"H:i" }}
{{ created|utc|date:"c" }}
{% now "DATE_FORMAT" as today %}
```

| Named format | English fallback |
| --- | --- |
| `DATE_FORMAT` / default `date` | `N j, Y` |
| `DATETIME_FORMAT` | `N j, Y, P` |
| `SHORT_DATE_FORMAT` | `m/d/Y` |
| `SHORT_DATETIME_FORMAT` | `m/d/Y P` |
| `TIME_FORMAT` / default `time` | `P` |
| `YEAR_MONTH_FORMAT` | `F Y` |
| `MONTH_DAY_FORMAT` | `F j` |

Empty or falsey filter arguments use the filter's default; an explicit empty
`now` format still produces empty output. `time` accepts clock tokens and
`e O T Z`; unescaped date tokens render an empty
value. Non-time input also renders empty. Invalid UTF-8, NUL, a format over 4,096
bytes, expansion over 256 KiB, offsets outside the open range −24 to +24 hours,
or an instant outside years 1–9999 causes a render error with no partial HTML.
Other characters remain literal. A preceding backslash protects a token;
literal runs are unescaped once, preserving trailing backslashes and the Django
multiple-backslash behavior.

`c` preserves Go precision: nonzero fractions use six digits when microsecond
aligned, otherwise nine digits. UTC uses `+00:00`; historical second-resolution
zone offsets are retained. `u` deliberately displays only six microsecond digits.
`U` truncates pre-epoch fractional seconds toward zero without floating-point
conversion. `W` is unpadded and `o` uses the corresponding ISO week year.

Raw `time.Time` values mean instants. The engine cannot infer that an application's
untyped time value originated from a date-only or time-only model field. Such
calendar-only projection, locale-specific formatting and complete temporal
template conformance are not claimed by this checkpoint.

Regression tests enumerate every token, all day suffixes, Gregorian century
boundaries, ISO week/year transitions, DST overlaps, historical offsets,
pre-epoch fractions, escaping, named defaults and date/time separation. Fuzzing
checks malformed formats, bounded expansion and no partial output on error.
