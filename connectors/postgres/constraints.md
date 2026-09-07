# Declared constraint migrations

| Change | Historical state and PostgreSQL behavior |
| --- | --- |
| Add unique/check constraint | `Detect` emits `AddConstraint` after the same model's required field changes. Native constraints and ownership comments are created atomically. |
| Remove constraint | `RemoveConstraint` verifies the exact current descriptor and catalog ownership before dropping it. |
| Replace a definition | Remove then add inside an atomic migration. Failed validation restores the old constraint and does not append migration history. |
| Reorder declarations | Preserve exact state order without DDL. |
| Reverse add/remove/replacement | Replay cloned historical descriptors, including field order, deferral and null semantics. Optional operation metadata preserves older migration checksum formats. |
| Conditional unique | Use a guarded partial unique index, including `NullsDistinct`; never treat it as a native PostgreSQL table constraint. |
| Rename a unique field | Map logical names to physical columns once; refresh native constraint ownership after a physical column rename, preserving index identity. An unchanged explicit column name preserves the marker. |
| Remove a constrained column | Remove dependent declared constraints first. The editor does not silently discard constraints that reversal would lose. |
| Raw check/predicate SQL and column changes | Do not guess SQL dependencies or rewrite fragments. Remove/recreate the intended constraint explicitly before physical renames or type changes. |
| Unmanaged/proxy/abstract schemas | Preserve state, with no constraint DDL. Canceled operations still return cancellation. |

Native constraint markers bind the physical descriptor and PostgreSQL's canonical
definition. Removal checks the schema/table identity, constraint kind, validation,
deferral and inheritance flags. Unique constraints additionally verify their
backing index's ordered columns, default operator classes and collations,
uniqueness, null behavior and readiness. Comments and destructive statements name
the current schema explicitly. Temporary objects cannot redirect those changes;
a shadowed target table fails closed.

Missing, unmarked or changed constraints require explicit reconciliation. This
includes constraints created before ownership comments existed, and PostgreSQL
upgrades that change canonical definition rendering. Name-based adoption is not
performed. Application raw SQL is trusted schema code, never request input.

`db.ConstraintLifecycleEditor` is optional for future connectors. Migration
preflight rejects unsupported providers and non-atomic constraint operations
before DDL. Check `Fields` remain explicit ORM validation/exclusion dependencies;
the SQL expression defines database enforcement. Unsupported check `Condition`,
`Deferrable`, `NullsDistinct`, and unique `Expression` metadata are refused rather
than ignored. Model option transitions, expression-based unique constraints,
online constraint validation and automatic repair are separate features.
