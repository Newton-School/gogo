# Explicit resource creation

`Resource.CreateHandler(CreateOptions)` builds a POST handler for explicit
registration in an app's `urls.go`. Read-only `Resource.Routes` never enables
writes automatically. The create endpoint accepts bounded JSON;
[PUT/PATCH](update.md) is separately enabled. Generic delete and nested/file/M2M
persistence remain unfinished paths.
Durable operation keys are explicitly enabled with `CreateOptions.Idempotency`.
Without that configuration, operation-key headers are rejected. Query arguments
and conditional `If-Match`/`If-None-Match` headers are not supported on creation.

Provide an explicit input serializer and a factory returning a fresh typed
model matching the registered schema. `Prepare` supplies server-owned values
such as tenant identity. The mandatory `ValidateWrite` checks the full proposed
record after defaults and model hooks. It must be read-only and enforce current
write authority, not merely UI visibility. The default model policy requires
`app.add_model`; view permission is not additionally required for the
explicitly allowed create representation.

The handler authenticates, checks action permission and parser/Accept limits,
then owns the outer transaction. Serializer validation and the typed model's
`Clean` method run in mutable pre-encoding preparation; normal Store save hooks
remain active. Normalized and derived values therefore reach SQL, and callable
primary-key defaults are frozen only after preparation. The final save Guard
checks authority without modifying the candidate. Invalid serializer
or model fields return 422. Foreign-key existence is validated, and every
to-one target is rechecked and locked under its own mandatory query scope
after hooks. Changing a reference after its initial serializer validation
cannot bypass that scope.

`Audit` is required and receives only the action, object key and submitted
field names, never field values or credentials. Persist it using the supplied
transaction context and resource backend. Durable external work belongs in an
outbox on that same transaction, not a direct network call. `Location` returns
the explicit local URL for the newly inserted object. Unsafe locations,
serialization failure, audit failure, retargeted identities or later policy
changes roll back the model and audit together.

RETURNING identity is captured before application after-save hooks. Current
scope and the exact stored snapshot are checked again after representation,
audit and final write policy. Output uses the Resource serializer and its
field-level restrictions; model registration never exposes all fields.
`Scope` is a trusted read-only predicate supplier, not a mutation hook; it must
not change rows or grants, directly or through nested domain calls.

Cookie/custom identities require CSRF tokens. A nil CSRF config uses secure
cookies; development must explicitly configure otherwise. Only verified,
active scope-constrained bearer identities skip CSRF, never the presence of an
Authorization header alone. No application-supplied CSRF exemption is accepted.

The request body defaults to 1 MiB (up to 10 MiB); mutation timeout defaults to
30 seconds (up to five minutes). Server read/time limits still govern transport
before the mutation starts. Responses are private/no-store and a confirmed
create returns 201 with Location. `X-Gogo-Mutation` reports `unchanged`,
`committed` or `unknown` for requests that reach mutation processing. A failed
after-commit callback can produce an error with `committed`; an uncertain
commit returns 503 with `unknown`, no success body and no Location. This
endpoint never automatically retries a write: reconcile an unknown result
before resubmitting. With durable operation keys enabled, retry the same key
and same arguments to reconcile; never generate a replacement key on error.

## Durable HTTP operation keys

Install `IdempotencyMigrations()` explicitly on the resource backend and set
`CreateIdempotencyOptions` with a stable mutation-contract `Version`, trusted
tenant/resource `Scope` and read-only `Redact` policy. A version identifies
the operation family; do not change it when a user's grants change. Actor
identity is independently bound by the receipt service. Callbacks must be
immutable/concurrency-safe and honor cancellation.

Exactly one `Idempotency-Key` is required by default (1–255 printable ASCII
characters without spaces). `Optional: true` deliberately permits ordinary
unkeyed writes; it does not infer keys. Method, host, escaped route path and
parsed JSON are hashed into the operation arguments. `Vary` supplies additional
application semantics such as middleware-selected locale; never put credentials
in it. JSON numeric equivalence and exact large integers are preserved.

One transaction claims the key and commits the typed model, audit and bounded
receipt together. Concurrent identical keys return one logical creation.
Different arguments under that key return 409; they never run a new mutation.
Confirmed replays return the original 201 and Location with
`Idempotency-Replayed: true`. Current model/object add permission, proposed-write
policy, root row scope, related-target scope and output field visibility are
checked again under row locks. A hidden/deleted root gives 404, not the old
receipt. The current grant version does not create a fresh operation identity.

`Redact` receives a current scoped record and a detached original body. It must
enforce visibility of nested/computed fields and may only remove original
fields, never recompute values. Resource serializer/field restrictions apply
additionally. In-memory record changes and nested writes to the root or related
targets are rechecked before commit. A failed replay cannot update its stored
receipt or the business object. Read-only callback contracts still prohibit
unrelated writes; this is not a sandbox for application code.

Retention defaults to 24 hours; expired keys can create new operations. Do not
use receipts as an indefinite exactly-once guarantee. Physical receipt cleanup
is not yet provided. See [the receipt contract](idempotency.md) for bounds and
transaction outcome semantics.
