# Explicit resource updates

`Resource.UpdateHandler(keyDecoder, UpdateOptions)` returns an explicit PUT/PATCH
handler for registration in an app's `urls.go`. `Resource.Routes` remains
read-only. Authentication, CSRF, bounded JSON parsing and transaction outcome
reporting use the same boundary as [creation](create.md).

Supply an input serializer with an explicit field allowlist, a fresh typed
model `Factory`, a read-only `ValidateWrite` policy and an atomic `Audit`
callback. The key decoder returns exactly the registered primary-key fields;
malformed or out-of-scope identities return 404. Decoder/provider cancellation
remains a failure, never a successful lookup or a not-found result.

This path updates managed concrete models and stored scalar, JSON and to-one
fields. Primary keys, generated fields, files, nested writes, collection writes,
proxy/unmanaged models and multi-table inheritance are not implicitly writable.
The constructor rejects unsupported input instead of silently ignoring it.
Read-only serializer fields remain ignored on input, not exposed as secrets.

## Presence and model validation

PUT performs full input validation: required fields must be supplied and
serializer defaults apply. PATCH validates only supplied fields and does not
apply defaults to omitted fields. Explicit null, zero, false and empty values
are distinct from omission. Omitted values without a serializer default retain
their current stored values; this is not a reset-all-columns operation.

`FromModel` makes nullable fields optional unless a uniqueness requirement
requires input. This does not make explicitly declared plain serializer fields
optional. Configure overrides for application-specific requirements. These
presence rules follow the documented [serializer partial-update contract](https://www.django-rest-framework.org/api-guide/serializers/#partial-updates)
and [model-derived required-field rules](https://www.django-rest-framework.org/api-guide/fields/#required);
they are not a claim of complete serializer compatibility.

The handler locks the currently scoped root, copies its stored values into a
fresh typed model, binds validated input, and runs server-owned `Prepare`.
Normal save hooks and `FullClean` normalize and validate the candidate before
encoding. Proposed and final state must pass current change permission and
`ValidateWrite`. Each to-one target is separately scoped and locked. Explicit
eager paths retain the detail representation shape, including nullable targets;
the root uses `FOR UPDATE OF self` where required by the backend.

Primary-key retargeting, changed persistence identity and nested writes that
would be silently overwritten fail closed. Snapshots fence stored root changes
after hooks, representation, audit and final policy. Read-only callbacks must
not perform unrelated writes: these checks are not a sandbox for application
code. Serialization and audit occur before the outer commit, so their failure
rolls back the model and audit together. Outbox work must use that same
transaction context, never an external network call inside the callback.

## Conditional updates

Enable `ResourceConfig.EntityTags` to emit a strong ETag on an authorized detail
GET/HEAD. The digest identifies the exact emitted JSON bytes, not hidden model
fields or a multi-query database snapshot. Every read still performs current
authorization before evaluating conditional response headers.

PUT/PATCH accept `If-Match` only when entity tags are enabled. The comparison
runs under the root lock against the currently authorized representation.
An unmatched tag returns 412 without a model or audit write. Weak tags never
satisfy this strong comparison. Repeated header lines and commas inside opaque
tags are parsed as entity-tag syntax, with bounds of 32 lines, 4096 bytes and
64 list elements; malformed conditions return 400.

`RequireMatch: true` rejects a missing condition with 428. Standard `If-Match: *`
means an existing scoped object, not a particular revision; require specific
tags at the application boundary if wildcard updates are inappropriate. Other
conditional headers and query parameters are rejected on this mutation path.

Successful updates return 200 with the authorized representation, but no ETag:
normalization can transform input, and replay-time redaction can change visible
fields. Read detail again for a fresh validator. This avoids claiming an
untransformed PUT representation under [HTTP validator rules](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.3.4).

## Receipts and outcomes

Install `IdempotencyMigrations` and configure `UpdateOptions.Idempotency` with
`MutationIdempotencyOptions` to require durable operation keys. `Optional: true`
permits unkeyed updates deliberately; an unconfigured handler rejects key
headers. The create-specific option name remains a compatible alias.

Update receipts bind actor, stable resource scope, operation-contract version,
method, host, escaped path, parsed body, decoded object key, the submitted
If-Match header values and explicit `Vary` semantics. Changing arguments under
an existing key returns 409. Replaying the same key and original precondition
returns the original 200 without updating the model or adding another audit,
even when the original write made that precondition stale. Current change
authority, scope, related-target visibility and removal-only receipt redaction
still apply; receipts are not authorization bypasses.

`X-Gogo-Mutation` reports `unchanged`, `committed` or `unknown` once mutation
processing starts. Pre-commit failure rolls back; a genuine after-commit
failure may report an error with `committed`. An uncertain commit reports 503
with `unknown`, never a success body. Reconcile with the same key and arguments,
not a replacement key. Unkeyed writes are never automatically retried. Retention
and the remaining cleanup limitations are described in [operation receipts](idempotency.md).
