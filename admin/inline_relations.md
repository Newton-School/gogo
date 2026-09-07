# Scoped inline relationship forms

| Parent feature | Inline behavior |
|---|---|
| Fields | Editable `ForeignKey` and `OneToOne` fields in `Inline.Fields` use ordinary model-choice selects. The server-controlled `FKName` cannot be an editable inline field. |
| Target registration | Register each select target on the same Admin site and provide `Inline.ResolveRelation`. The resolver must return the submitted primary-key or declared unique target-field identity exactly. |
| Scoped choices | Load one bounded target candidate list per field per inline configuration, shared across its rows. Model/object view policy, the target store scope, and the resolver all apply. More than 1,000 candidates fails closed. |
| Empty/hidden values | Always render an explicit blank option. Required validation remains required; optional nullable relations can clear. Never append an out-of-scope stored ID to the options. |
| Overrides | Existing choice IDs can narrow eligibility; labels are still obtained from the authorized store. Custom widgets, disabled fields, configured readonly fields, and object-level readonly rows keep their existing modes. No new parent-only raw/autocomplete configuration is added to `Inline`. |
| Binding | Management counts and complete scoped row identities are validated by the existing formset. Every editable ordinary selection is reloaded through exact scoped queries during formset and model-form cleaning. Provider errors remain operational failures, not successful validation. |
| New rows | Initialize the scoped child, bind declared fields, and assign its parent FK on the server. Ignore unused empty extra rows. Never trust a submitted parent identity. |
| Existing rows | Check the signed row edit token, scope, parent ownership, and change permission. A readonly decision during initial choice preparation or formset binding remains restrictive for the rest of that request; a later policy callback cannot reopen that row with unprepared target authorization. Snapshot cleaned ordinary FK values before save callbacks. |
| Deleted rows | Retain the existing deletion collector, graph authorization, protected-relation handling, and audit. Deleted rows do not need valid editable relation input and are not included as saved-row fences. |
| Audit privacy | A previously nonempty ordinary relation that is no longer eligible is omitted from that field's entire audit diff, not represented as a false null. Other changed inline fields remain audited. |

## Atomic callback and final-check ordering

| Phase | Parent and all inline groups | Failure outcome |
|---|---|---|
| 1 | Lock/load parent; bind parent form, inline formsets, and each writable inline model form; freeze the cleaned ordinary FK selections. | Validation response or safe operational error; no saved changes. |
| 2 | Reauthorize the parent's selections; save the parent and its existing relation operations. | Roll back the transaction. |
| 3 | Freeze the server-controlled parent FK for **every** inline group before any child save callback. | Roll back the transaction. |
| 4 | In each group, assign the frozen owner FK, reauthorize each writable row's selected targets, save and audit the row; then execute authorized inline deletions and their audits. | Roll back parent, all rows, relation operations, and audit. |
| 5 | Finish `SaveRelated`, account-related operations, parent audit redaction, and parent audit. | Roll back the transaction. |
| 6 | Reauthorize selected targets for the parent and **all written inline rows**. Complete every policy/resolver callback before continuing. | Roll back the transaction. |
| 7 | Prepare fresh source and selected-target store scopes for the parent and **all written inline rows**. Complete every scope callback before continuing. | Roll back the transaction. |
| 8 | Read the locked source records: identity/model must match; every selected ordinary FK must match its immutable pre-save value; every written inline's stored owner FK must equal its frozen server parent. Read every selected target through its prepared scope: identity/model/relationship value and stored-field fingerprint must match the authorized target. No further policy/resolver/scope callbacks run in this phase. | Roll back on disappearance, retargeting, scope changes, fingerprint changes, provider errors, or cancellation. |
| 9 | Commit the existing transaction only after **all** final reads pass. | Database commit failure remains a failed save. |

A complete check is intentionally **not** run one row at a time: the last row's resolver or scope callback could otherwise mutate a source or target whose final check already passed.

The final fence covers ordinary selected FK/O2O fields and the server-owned parent link of every written inline. It does not turn readonly rows into writes or fence arbitrary hook-derived scalar fields. Custom stores must honor exact filters and transaction-held row locks and keep low-level reads free of application mutation callbacks. Resolvers remain responsible for locking/fencing additional domain state or external services used in their authorization decisions.

Choice rendering is bounded, not constant-cost: each policy-visible candidate invokes the application resolver once per field/group; each row renders its own option markup. Formset cleaning, model-form cleaning, pre-save checks, eligible-prior audit checks, and final authorization each perform exact selected-ID queries instead of rescanning all choices.
