# Field database indexes

`models.WithDBIndex(true)` requests an implicit B-tree index on a stored field.
`models.SlugField` requests one by default and defaults to length 50, matching
the documented [Django field options](https://docs.djangoproject.com/en/6.0/ref/models/fields/#slugfield).

| Operation | PostgreSQL effect |
| --- | --- |
| Create model / add field | Create the plain index and its framework ownership comment atomically. The normal migration transaction owns the complete model/field operation. |
| Effective scalar PK, `Unique`, or one-to-one | Do not add a redundant plain index. A composite PK does not suppress each requested single-column index. |
| Explicit application index / constraint | Preserve its independent identity. Implicit field indexes never replace an explicitly declared resource. |
| Set `DBIndex` to true | Create the owned index; a database name collision fails, never overwrites. |
| Set `DBIndex` to false | Lock the table, verify ownership and exact plain-index shape, then drop only that index. |
| Rename field with implicit column | Rename the column, owned index name and ownership marker together, retaining the index OID. |
| Logical rename with explicit `db_column` | Keep the physical column/index untouched; update historical logical references. |
| Change explicit `db_column` | Rename the column and owned index using the same guarded boundary. |
| Validator/presentation-only metadata | Keep migration state/checksum changes, but emit no column TYPE rewrite or index DDL. |
| Reverse | Restore prior index metadata and names; failed transactional migration leaves no new index, marker or history row. |

Implicit names contain a stable table/column hash and stay within PostgreSQL's
63-byte identifier bound. Catalog guards verify the marker, owning table and
column, B-tree/default operator class, default column collation, one ascending
key with no included columns, no expression/predicate, non-unique/non-primary,
and ready/valid flags. Validation and alteration run under one table lock.
Index names are schema-qualified during create, comment, rename and drop, so
temporary objects cannot shadow them. A shadowed or non-current-schema target
table is rejected before an implicit index operation can commit.
SQL previews include the guard without querying or modifying the database.

Older Gogo versions retained `DBIndex` metadata without creating an index.
A missing, unmarked or structurally different index therefore requires an
explicit developer reconciliation migration; a historical boolean is not proof
that Gogo owns an existing database object. No `DROP IF EXISTS` or automatic
adoption of a matching application index is performed.

This is blocking migration DDL, not a concurrent-index rollout mechanism.
Collation/tablespace configuration, uniqueness/primary-key changes, relation
target changes and positive-value constraint changes are rejected when the
current schema path cannot enforce them. Logical field renames involving raw
SQL expressions/conditional indexes require explicit state and SQL operations;
the detector does not rewrite SQL fragments by string replacement.
Explicit logical renames run before other field comparisons and update inbound
`TargetFields` and intermediary `ThroughFields` in historical state, regardless
of model sort order. They do not synthesize independent relation alterations.
