# Durable idempotent operations

`Idempotency` is opt-in. Apply `IdempotencyMigrations()` explicitly before use;
imports never create tables. Configure a transactional backend, current
`Authorize` and `Redact` policies, then call `Execute` with a verified active
principal in its context. PostgreSQL is the current supported connector.

An `Operation` contains a stable tenant/resource `Scope`, versioned `Action`,
client `Key` (1–255 printable ASCII characters) and JSON `Input`. Include every
mutation argument in Input: body, route identity, method and conditional
headers. Exact numeric canonicalization makes 1 and 1.0 equivalent without
rounding large integers. Key/request digests, not plaintext keys or request
bodies, are persisted. The same user/scope/action/key refers to the same
operation after permission/auth-version changes; current authority is checked
again, not used to silently create another operation.

Execute owns the outer transaction and rejects nesting on that database alias.
Its mutation callback must use the supplied transaction context and backend
alias for the business write, audit and outbox. It must not perform direct
external effects. The receipt is claimed and row-locked, the callback runs at
most once for a committed unexpired identity, and its sealed response commits
with the business mutation. A different input for an existing key returns 409.
Concurrent claim/row-lock waits are bounded by the operation Timeout (30s
default, 5m maximum); callbacks must honor context cancellation.

Authorization runs with no object key before lookup and with the stored/new
object key before disclosure. The policy must enforce current object scope,
including how a deleted object's tombstone is authorized. Redaction runs on
an isolated copy on first response and every replay. It may remove object
fields, including fields inside array elements, but cannot replace values,
reorder arrays or add fields. Both policies are read-only. Only explicitly
public data belongs in the response; there is no automatic secret detector.

Responses are JSON objects with status 200/201, or an empty object for 204.
Only JSON Content-Type, a relative Location and a strong ASCII ETag can be
retained. Cookies and other headers are rejected. Responses and inputs are
bounded to 1 MiB, 32 nesting levels and 65,536 ordinary JSON nodes; number
tokens are bounded to 1,024 bytes. Numeric response formatting is canonical on
both first response and replay, independent of JSON database formatting.

Inspect `IdempotencyResult.Outcome` **and** the returned error. Committed
after-commit callback failures retain the sealed result with `committed`;
confirmation comes from this outer transaction's own commit marker, not merely
a callback's error type. Unknown commit acknowledgements return no response
and `unknown`: retry the same operation identity to reconcile. A failed
callback/serialization rolls back both mutation and receipt. The service never
retries mutation callbacks itself. Application panics within the transaction
body become safe callback errors before Commit and roll back normally. A panic
in transaction commit/cleanup without confirmation remains conservatively
unknown; no panic value is disclosed.

Retention defaults to 24h and cannot be shorter than Timeout. An expired key
may perform a new mutation; replay is only guaranteed inside retention. Expiry
is checked under the same row lock. [Resource creation](create.md) and
[PUT/PATCH updates](update.md) integrate these receipts through explicit
mutation options; generic delete integration remains unfinished.
Do not expose `IdempotencyRecord` through generic Admin/resources or logs;
direct JSON serialization and routine formatting are deliberately restricted.

## Expired receipt maintenance

`service.Prune(ctx, limit)` removes one oldest-first batch of expired sealed
receipts, without loading response bodies or calling business model hooks.
Zero selects a 100-row limit; explicit limits are 1–1000 and must fit the
connector parameter budget. Expiry uses the same configured trusted `Now`
clock as Execute, never an arbitrary caller-supplied cutoff. Unexpired and
unsealed rows are retained; no business objects, audit rows or outbox records
are deleted. There is no implicit background cleanup or public HTTP endpoint.

Prune is disabled unless `IdempotencyConfig.AuthorizePrune` is configured.
That read-only policy explicitly authorizes expired-receipt deletion across
**all actors and scopes on this backend**. Supply a verified active maintenance
principal, not an ordinary user's resource grant. The policy runs inside the
owned outer transaction before selection and again after the batch is locked;
denial or cancellation aborts the entire batch. Ambient same-alias transactions
are rejected, and all provider/lock waits share the service Timeout.

Prune locks selected receipts in expiry/ID order and rechecks expiry in its
delete statement. Execute cannot renew a locked receipt while it is deleted;
a concurrent renewal that wins first is retained. A bounded lock timeout leaves
maintenance unchanged. Competing batches can observe fewer rows; a short or
empty batch is not a global completion signal while requests are concurrent.

Check both `PruneResult.Outcome` and the error. `Deleted` is confirmed only for
`committed`, including a failure in an after-commit observation. `unknown`
deliberately reports zero, without asserting that nothing was removed. Another
bounded maintenance pass is safe for expiry but cannot reconstruct the exact
previous count. Removing or reusing an expired receipt ends its replay guarantee;
the same client key can subsequently perform a new business mutation.
