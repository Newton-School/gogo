# Named index migrations

| Client change | Migration behavior |
| --- | --- |
| Add `Schema.Indexes` entry | Detect an `AddIndex` after required field changes. |
| Remove a named entry | Detect a table/descriptor-aware `RemoveIndex` before removing fields. |
| Remove an indexed field explicitly | Remove its named indexes first. The editor refuses implicit loss of key/include dependencies or any unresolved raw predicate. |
| Change an existing name's definition | Guarded remove followed by add in one atomic migration. A failed replacement restores the old index and leaves no history row. |
| Change declaration order only | Record exact historical order without emitting DDL. |
| Rename an indexed field | Update logical references; for a physical column rename, verify and refresh dependent named-index ownership in the same transaction. Explicit unchanged `db_column` preserves index identity and marker. |
| Reverse add/remove/replacement | Replay the immutable prior descriptor. Old/new slices and optional values never alias caller metadata. |
| Change a concurrent index | Detection refuses automatic conversion. Use an explicitly designed online migration; Gogo never promotes a migration to non-atomic execution. |
| Change declared constraints | Use the separate [constraint lifecycle](constraints.md), not ordinary named-index removal. |
| Change model options | Return an actionable unsupported-operation error; do not silently report no drift. |
| Index operations on unmanaged, proxy or abstract schemas | Preserve migration state without executing index DDL, matching model creation. |

Nonconcurrent `SchemaEditor.AddIndex` creates the index and its ownership comment
in one PostgreSQL statement. The marker binds the physical descriptor (including
mapped key/include columns, uniqueness, method, predicate and null semantics)
and PostgreSQL's canonical index definition. Destructive operations verify the
current marker and definition, table and namespace, key/include order, default
operator classes/collations, uniqueness/null behavior, and valid/ready flags.
Constraint-owned indexes cannot be removed as ordinary named indexes.

Create/comment/drop names are schema-qualified. Temporary index objects do not
shadow them; a shadowed target table fails ownership validation. Missing,
unmarked or changed indexes require explicit reconciliation, including indexes
created by an older Gogo release before ownership comments existed. A PostgreSQL
upgrade that changes canonical definition rendering can also require explicit
reconciliation; no name-based adoption or silent marker repair occurs.

The public optional `db.IndexLifecycleEditor` contract adds descriptor-aware
removal without expanding every connector's required `SchemaEditor` interface.
Migration preflight rejects missing support before DDL. The legacy explicit
`SchemaEditor.RemoveIndex(name)` remains a low-level name-directed operation;
framework migration reversal does not use it.

Explicit concurrent creation retains its separate, non-atomic creation path;
it does not receive an atomic lifecycle ownership claim. Raw predicate SQL is
never rewritten to guess renamed fields. A physical column rename or type change
while any conditional index remains, and concurrent-index field-column renames, require an
explicit migration. Remove dependent indexes first and recreate the intended
definitions after changing columns.
