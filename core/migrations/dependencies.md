# Cross-model migration ordering

Detection first builds historical operations, including explicit rename
normalization. A stable dependency graph then orders their database effects.
This graph covers native foreign-key and one-to-one fields. Automatic
many-to-many intermediary dependencies are not traversed by this planner slice;
their creation/cleanup remains a separate schema-editor lifecycle.

| Dependency | Forward order | Reverse order |
| --- | --- | --- |
| FK to a newly created model | Target table before referencing table | Referencing table before target table |
| FK to a newly added target field or unique key | Target field and unique constraint/index before FK field | Remove FK field before its target uniqueness/field |
| Referenced model/field/key removal | Remove referencing FK field/table first | Existing irreversible model/field deletion rules still apply |
| Self-reference inside one new model | Keep one `CreateModel`; no false cycle | Drop that same model normally |
| Independent operations | Preserve original deterministic ready-operation order | Reverse the resulting order |

Each model's internal operation sequence and snapshots remain intact. Every
rename runs before ordinary field/index/constraint differences. Detection does
not read the database, modify caller metadata, or change the migration graph's
declared atomic boundaries. `NoConstraint` relations and non-creating model
descriptors do not invent physical creation dependencies. Metadata-only index or
constraint removals on nonmanaged schemas are not storage drops; deleting an
unmanaged descriptor cannot count as removing its native database FK.

Cycles or conflicting intra-model requirements return an explicit diagnostic and
no executable operation list. This slice does not split cyclic creates/drops
into separate FK DDL. A retained FK may depend on a particular unique backing
index: removing or replacing that key requires explicit staged relation work,
even if another unique object covers the same fields. No automatic retargeting
is assumed. New FKs to explicitly removed models or missing fields on a supplied
current target are rejected before any executable plan is returned. Dependencies
outside supplied historical/current descriptors remain
the database's enforcement responsibility; no catalog state is guessed.
