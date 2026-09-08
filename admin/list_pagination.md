# Bounded change-list pagination

`ModelAdmin.ListPerPage` controls ordinary pages (default 100).
`ListMaxShowAll` controls the largest complete filtered result that can be shown
on one page (default 1,000). Existing registration bounds remain 1–1,000 rows,
with the show-all cap at least the normal page size.

| Request or state | Behavior |
| --- | --- |
| Ordinary list | Read `ListPerPage` rows; preserve normal previous/next navigation |
| Filtered scoped count exceeds one page but is within cap | Offer a Show all link |
| One empty `all` parameter (`?all=`) | Read at most cap + 1 rows at offset zero; the extra row detects overflow independently of the count |
| Nonempty/repeated `all`, or `all` combined with `p` | Reject with 400 before listing; `all` is reserved and cannot also name a list filter |
| Count or actual rows exceed cap | Reject with 400; never render a truncated result as all records |
| Count differs from returned row count | Reject with 409; a changing/inconsistent filtered set requires a fresh request |
| Duplicate/missing row identities or provider failure | Fail before computing or rendering display values; no unfiltered fallback |
| Empty filtered result | Show the existing empty state and return-to-pagination link |
| Search/filter/sort navigation | Preserve the mode and allowlisted parameters; mode switches clear the page number |

Counts and rows come from the same trusted `ScopedStore.List` query contract.
The ORM adapter still performs separate count/read statements; this is not a
cross-request snapshot or a guarantee against concurrent changes with the same
count. The overflow sentinel and exact returned-count comparison prevent a
known partial set from being labeled complete. Hidden rows never enter counts,
navigation or display. Invalid query encoding and unknown parameters fail
closed, including on direct URLs.

List editing uses the same explicit mode: the signed manifest binds the entire
query, exact row identities/versions and configured fields. Ordinary-page
tokens remain limited by `ListPerPage`; show-all tokens and POST rows are limited
by `ListMaxShowAll` (never the overflow sentinel). Changing mode/filter/order,
exceeding the cap, changing rows or invalid forms does not partially save.
Existing whole-batch permission, callback, transaction and audit fences remain
in force. Successful redirects preserve the mode and query.

Show all is presentation, not an action's select-all grant: ordinary explicit
checkbox selection and existing action permissions/limits are unchanged.
The default template uses normal keyboard-operable links inside the existing
named pagination navigation. No JavaScript or browser-only authority is added.
