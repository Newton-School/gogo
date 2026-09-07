# Transactional deletion callbacks

`DeleteCollector.Execute` locks and collects the current scoped graph inside
`db.Atomic`. The execution graph stays private. Preview `Collect` is unchanged;
its result is neither a deletion command nor a reusable authorization decision.

| Boundary | Behavior |
| --- | --- |
| `Scope` | Receives detached schema metadata, not the descriptor later used by the SQL compiler. Predicate decisions remain authoritative; editing that metadata cannot retarget columns or relation defaults. |
| `Authorize` | Receives a detached graph of objects, relation updates and automatic-join removals. Changed records, state, update values or descriptors return `ErrDeleteCallbackMutation`. |
| `BeforeDelete` / `AfterDelete` | Each receiver gets a fresh read-only `models.Record`. It does not expose `models.Underlying` or a saveable model. Changed current record values/state return the same sentinel. |
| Captured earlier view | Later changes cannot affect another receiver or the private SQL plan. Deletion still targets the originally authorized rows. |
| Schema metadata | `Schema()` returns a fresh metadata copy. Editing that disposable copy changes neither the callback record nor SQL. Registered validators, default functions and codecs remain trusted runtime services. |
| Trusted business/audit hook | May use `db.ExecutorFor(ctx, backend)` for same-transaction effects. A denied/mutated hook, failed SQL statement or commit failure rolls back all transactional writes. Hooks are trusted Go code, not a sandbox. |
| Success | Counts reflect the original private plan. Caller-root persistence state and primary keys are cleared only after the transaction succeeds. |

Callback snapshots preserve field values, concrete container types, cycles and
repeated exact aliases within a view. Byte slices, maps, pointers and exported
struct data are detached recursively. Separate deliveries do not preserve pointer
identity; overlapping subslices need not retain their shared backing-array
relationship. Neither case shares mutable data with the execution graph.

The snapshot copier never calls `MarshalJSON`, `driver.Valuer`, codec methods or
other application methods. Scalar-only private struct fields can be copied, and
`time.Time` receives its own publicly writable location object: the instant, wall
time and location rules are preserved, but process-local monotonic clock metadata
is deliberately dropped. Persisted database timestamps do not carry monotonic
readings. A private struct field containing a timestamp cannot be safely replaced
through reflection and is treated as opaque. A map whose distinct timestamp keys
would collapse after monotonic normalization is rejected, never silently reduced.
Non-nil private reference-bearing data,
channels, functions and unsafe pointers cannot be safely detached. Snapshot work
is bounded to depth 128, approximately one million visited values and a 64 MiB
estimated allocation budget; equality checks are also bounded. Values that exceed
these limits return `ErrDeleteCallbackView` before authorization, hooks or deletion
writes. Hooks should persist identifiers and selected scalar audit data, not keep
record views across goroutines.

These limits apply only when a callback view is required. Without authorization or
delete receivers, existing custom-codec row values continue to work; a configured
scope only requires safe detachment of its schema metadata. There is no new
codec cloning interface. Unchanged callable schema metadata, NaN values and
pointer-keyed maps do not spuriously count as mutations.
