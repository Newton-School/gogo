# Search guidance

`ModelAdmin.SearchHelpText` adds plain-text guidance below the change-list search
input when `SearchFields` is nonempty. It defaults to an empty string, preserving
the existing search layout. A nonempty value is connected to the search input
with `aria-describedby` and a single stable `search-help` ID.

Registration snapshots the string and rejects more than 4,096 UTF-8 bytes,
invalid UTF-8, or NUL characters, even if search is currently disabled. HTML-like
text is allowed as text and escaped by the default template, never marked safe.
No help element or description attribute is emitted for empty text or disabled
search. Ordinary template overrides retain their existing responsibilities.

This option only explains existing search behavior. It cannot enable search,
change scoped queries/counts, allow new URL parameters, or affect sorting,
show-all mode, list-edit validation or authorization. Invalid list-edit forms
retain the same accessible guidance, errors and single management token set.
