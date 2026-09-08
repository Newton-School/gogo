# Historical relation transitions

`SchemaTransitionEditor.WithSchemaTransition(before, after)` is an optional
public connector contract. Each migration receives separate cloned starting and
ending model snapshots. SQL preview and application use the same resolver;
reverse supplies those snapshots in reverse order. The original migration
descriptors and checksums are unchanged.

Default, bound, and choice maps, slices, arrays, and plain pointers are detached without
calling application encoders or defaults. Custom struct objects, callbacks,
and opaque runtime values remain trusted configuration and must not be mutated
by a schema editor. This is not a sandbox for connector/application code.

| Operation | PostgreSQL metadata and effect |
| --- | --- |
| Create model / add automatic M2M | Ending target metadata; queue the generated bridge until ordinary tables exist |
| Delete model | Starting, generated automatic bridge inventory; drop matching bridges once, then the model |
| Remove automatic M2M | Starting target metadata; drop the old bridge or cancel its pending creation |
| Remove then recreate source | Old physical table and new physical table stay separate, even for the same logical model key |
| Transient relation add/remove | Cancel pending bridge creation; historical bridge removal remains recorded |
| Reverse create / add | Use the original ending inventory as the reverse starting inventory |
| Explicit through / arbitrary auto-created labels | Never adopted into automatic bridge deletion inventory |
| Later statement fails in an atomic migration | PostgreSQL restores bridge/model tables; migration history is not advanced |

A connector without this optional contract is refused before any database I/O
when a forward operation removes an existing automatic relation, including a
remove/recreate with identical boundary descriptors. Existing single-snapshot
resolvers remain usable for unaffected migrations and their supported reverse
operations. Returned editors isolate pending creations and completed drops per
migration execution; a retry resolves fresh state.

Starting/ending snapshots do not describe a target model created **and deleted**
inside the same migration. Automatic relations to that transient target are
refused before DDL, including in a non-atomic migration; split that graph into
separate migrations. A transient relation using a target present at a boundary
can be canceled normally. Native dependency cycles, automatic-M2M dependency
planning, and online migration semantics remain separate capabilities.

Deleting a model or removing a field is still explicitly irreversible: rolling
back failed transactional DDL is not restoration of successfully deleted data.
These changes add no CASCADE, catalog-name adoption, or implicit ownership of
explicit intermediary models. Database constraints continue to reject retained
explicit inbound relations.
