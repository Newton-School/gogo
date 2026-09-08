# Changelist editing

Set `ModelAdmin.ListEditable` to edit existing records directly in the model list:

```go
admin.ModelAdmin{
    Schema: productSchema,
    Fields: []string{"name", "published"},
    ListDisplay: []string{"id", "name", "published"},
    ListEditable: []string{"name", "published"},
    ConstraintChecker: store,
}
```

This initial implementation supports stored scalar fields. Registration rejects primary keys, noneditable/readonly/excluded/sensitive fields, list links, fields absent from the form or display, custom display callbacks on editable cells, relation/file/image fields, and account models requiring domain-backed editors. `Fields` may come from `Fieldsets`. Ordinary `FormOverrides` customize field validation and widgets; those widgets must remain scalar forms compatible with the stored field. Custom changelist/formset hooks, relationship uploads, and account changelist editing are not implemented.

Each page uses existing scoped search, filters, ordering and pagination. A one-hour signed manifest binds the current actor/site/model, field names, exact page identities, version/snapshot digests and a hash of the query. It contains no field values, labels or raw filter/search text. Ordinary pages are bounded by `ListPerPage`; explicit [show-all pages](list_pagination.md) use `ListMaxShowAll` instead (both maximum 1000), subject to the signer's payload bound. A changed page or mode, expired token, identity substitution, duplicated management data or ambiguous action/save submission is rejected; reload before editing again. Unknown extra model fields and readonly row values are ignored, never assigned.

The entire submitted page is validated before opening the write transaction, then reloaded under scope and locked in deterministic identity order. Validation and view/change/readonly checks run again. Only changed writable rows invoke `SaveModel` (or `ScopedStore.Save`), `SaveRelated`, and a redacted audit entry. `SaveRelated` receives the changelist request, not a detail-form request. These save hooks must use the supplied transaction context for database effects and defer external effects until commit.

Permission, scope, readonly, widget and validation callbacks are read-only, may run multiple times, and must be cancellation-aware. They must not perform external writes. The editor checks all original, proposed, saved and untouched sibling snapshots across callback stages. After all save/related/audit and final permission callbacks, it resolves scope again and checks every batch row; a later callback cannot silently overwrite an earlier or untouched row. The store must provide genuine atomic transactions, scoped row locks and transactional auditing. Dynamic readonly removal of a changed field at the final boundary denies and rolls back the batch.

Successful writes return HTTP 303 with preserved query parameters and optional generic flash feedback. Field errors return 400 with per-cell errors; stale snapshots return 409. Scope/permission and provider errors use normal safe Admin responses. `X-Gogo-List-Change` is `unchanged`, `changed`, or `unknown`. Callback errors and panics contained before commit roll back this batch, even when a hook returns a foreign transaction's typed error. A lost commit acknowledgement or a panic outside the transaction body returns 503 with an explicit review-before-retry message. A failed callback after a proven commit reports `changed`.

The optional `ObservedAtomicStore` port witnesses this exact durable transaction before user after-commit callbacks. The ORM adapter implements it and rejects nested/ambient transactions before mutation: releasing a savepoint is not proof of durable success. Custom stores must honor rollback-on-body-error, and their ordinary `Atomic` success must mean a durable outer commit; after successful-body failures without an observer, a typed committed error is only `unknown`, never proof of `changed`. No batch, hook or audit is automatically replayed. Session/flash delivery is separate from the database transaction.

Automated tests exercise scoped PostgreSQL writes, mixed-row validation/audit failures, late saved/untouched/proposed sibling changes, final policy and scope changes, readonly revocation, and both uncertain and known-committed failure outcomes. Current browser proof for this new editor is still pending.
