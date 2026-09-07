# Changelist sorting

`ModelAdmin.SortableBy` controls user-selected sorting. `DisplayColumn.Ordering`
maps a computed display column to one stored model field. Registration copies
both declarations; requests never mutate them.

| Declaration | Behavior |
| --- | --- |
| `SortableBy: nil` (default) | Existing displayed stored fields and explicitly mapped display columns can be sorted. |
| `SortableBy: []string{}` | No sort links; direct nonempty `o` requests fail with HTTP 400. |
| `SortableBy: []string{"caption"}` | Only that registered displayed column can be selected, in either direction. Names must be unique unsigned identifiers. |
| `DisplayColumn{Name: "caption", Ordering: "name", Value: ...}` | `?o=caption` sorts by model field `name`; `?o=-caption` reverses it. The value callback renders cells, never supplies SQL. |
| `Ordering: "-name"` on a display column | The positive request uses descending `name`; the negative request uses ascending `name`. The header describes the effective field direction. |
| `ModelAdmin.Ordering` | Configured default ordering when `o` is absent or empty. Independent of `SortableBy`, including nondisplayed fields. |

Each mapping uses a logical model field name, not a database column name, raw SQL,
expression, relation path, or collection. New mappings accept stored built-in
numeric, Boolean, textual, UUID, file/path and temporal scalar fields; structured,
custom, generated and relation fields are rejected. Existing direct stored-field
sorting is unchanged. A custom display override of a stored field retains its
direct sort unless it declares another mapping or the allowlist disables it.

The changelist resolves the registered name and optional direction before its
scoped store read. It rejects unknown, denied or repeated ordering parameters;
direct URLs and editable-list POSTs use the same check. The selected mapping
replaces the configured order for that request. Every missing primary-key
component is appended ascending for stable ties, without duplicating a key
already present in either direction. Scope, search and filters still constrain
the database query before pagination. NULL placement and text collation remain
the connector's ordering rules.

Header links preserve search/filter parameters and reset the page. Only the
explicitly selected header announces its effective direction through `aria-sort`;
for a configured default, the first allowed visible matching column does so.
Inactive and disabled headers omit the attribute, including aliases of the
active column's stored field. This bounded
API does not add request-dependent `GetSortableBy`, arbitrary expressions,
relation sorting, multiple user-selected sort columns, or per-column NULL/collation
options. Sorting is not authorization: normal model/object policy and scoped
store checks continue to apply.

Coverage includes frozen registration, both signed mappings, allowlist and empty
policy, duplicate/invalid URL denial before reads, editable-list POST enforcement,
composite primary-key ties, link/accessibility metadata, and actual PostgreSQL
custom-column ordering with tenant scope, search and stable page boundaries.
No live browser verification is claimed for this checkpoint.
