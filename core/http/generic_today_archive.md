# Today's archive

`NewTodayArchiveView` selects today's calendar day from one captured clock reading. Configure the same explicit model, scope, field allowlist, pagination and template options as a [day archive](generic_day_archive.md); no date decoder or implicit route is involved.

```go
view, err := ghttp.NewTodayArchiveView(ghttp.TodayArchiveViewOptions{
    ListViewOptions: configuredListOptions,
    DateField: "published_at",
    AllowEmpty: true,
})
```

The private template locale resolver selects the calendar timezone. Otherwise the inherited locale or UTC applies. The host timezone is not a default. `Clock` defaults to `time.Now`; it runs once after request/model authorization and scope capture on each eligible GET/HEAD, never on method discovery or denied requests. The same instant determines both the date and the default DateTime publication cutoff, including around midnight and daylight-saving changes.

| Setting | Behavior |
| --- | --- |
| `AllowFuture: false` (default) | DateTime rows later than the captured instant are excluded; Date rows use today's calendar value. |
| `AllowFuture: true` | Later instants within today are eligible. The clock still runs once to select today; tomorrow is never selected. |
| `AllowEmpty: false` (default) | An empty first page returns 404; navigation uses at most four permission-checked occupied-period reads. |
| `AllowEmpty: true` | An empty first page may return 200; neighboring calendar dates need no row reads. Out-of-range later pages still return 404. |

All day-archive rules remain in effect: explicit fields, nullable-date exclusion, strict usable local midnight intervals, shared row/data limits, descending date plus stable primary-key ordering, final authorization, and no partial HTML after a provider or render failure. Navigation has no implicit database snapshot. Ambiguous or unsupported selected-day boundaries return 404; invalid clock instants or unrepresentable local clock years return 503.

Reserved template keys are `object_list`, `page`, `day`, `date_list` (nil), `previous_day`, `next_day`, `previous_month` and `next_month`. Neither the query nor URL can override `day`. Mount a separate `NewDayArchiveView` for dated links; the today route itself always selects the current local day. This constructor does not add month/week/year/index archives or editing views.
