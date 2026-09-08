# Explicit structured fixtures

`serialization.New` freezes a required backend alias and an explicit set of
model profiles. It does not discover applications, create tables, run policies,
or query data. Install migrations separately. Register the management commands
explicitly if CLI access is desired; this package installs no command or route.

This slice provides deterministic JSON/JSONL export and insert-only atomic
**non-auto-key** import. Export profiles may include auto primary keys.
`Import: true` rejects any auto field at construction, before reading input or
opening a transaction. There is no sequence-reset placeholder. Natural keys,
relations/forward references, XML/custom formats, updates/upserts, flush,
automatic initial-data loading and asset transfer remain unsupported.

## Profiles and authority

Each profile supplies its schema, an explicit `Fields` allowlist including every
primary-key field, mandatory root `Scope`, and mandatory `Authorize`. There is
no name-based secret detection or automatic all-fields export. `PolicyFields`
may add private grant inputs but never widen output. Every call supplies a
nonempty, duplicate-free `Models` subset; wildcard discovery is unavailable.

Policies receive fresh privileged `Record` snapshots: `PK` contains keys and
`Fields` contains the selected and policy-only **non-key** fields. Return exact
`ErrForbidden` for denial; other policy errors are unavailable failures. Denied
export records fail the export rather than silently disappearing. Schema and
scope predicate snapshots prevent later callback mutation from changing the
selected metadata or root filter. Opaque backend/dialect implementations and
opaque predicate parameters remain trusted cooperative ports, not deep-copied
application objects. Configure those ports before use; concurrently replacing
their internal state or mutating call inputs is unsupported.

Grants are cooperative, read-only application callbacks. They may inspect ACLs
using the transaction context but must not write data or schedule effects.
An independently mutable ACL must be locked/coordinated by its owner if the
application requires stronger revocation semantics than the read snapshot.
An exported byte cannot be recalled when authority is later revoked.

Supported selected fields are local scalar strings (including UUID), bounded
integers, boolean, finite float, decimal, JSON, binary, date/time/instant and
fixed duration. Selected custom codecs, relations, arrays/extensions, generated
columns and File/Image are rejected. Proxy/inheritance/unmanaged schemas are
not supported. Export may select a supported scalar subset of a larger model;
an import profile must explicitly list **every** schema field. Non-auto imports
require complete canonical keys (including composite keys); missing, duplicate
or conflicting identities never generate a key or overwrite an existing row.
UUIDs use lowercase canonical text. String keys are nonempty and at most 512
UTF-8 bytes. Integer keys retain their exact declared range.

## Version 1 wire

JSON is an array of records. JSONL contains one complete record per nonblank
line. Both formats use the same record shape:

```json
{"version":1,"model":"notes.Note","pk":{"id":{"value":42}},"fields":{"payload":{"value":null},"title":{"value":"Hello"}}}
```

Each cell is exactly `{"value":...}` or `{"sql_null":true}`. SQL NULL is permitted
only on nullable non-key fields. A JSON field's `{"value":null}` is actual JSON
null, not SQL NULL. Decimal values are exact strings padded to declared scale;
binary values use canonical base64; dates use `YYYY-MM-DD`, times use clock
strings, and instants use UTC RFC3339. Temporal precision is microseconds; years
1–9999 are supported for dates/instants. Durations are integer microseconds
within Go's duration range. Integers and JSON numbers never pass through
float64. JSON numbers use a bounded canonical decimal/exponent spelling so
database JSON normalization does not spuriously change a stored value.

Object duplicate keys, unknown record/schema names, incomplete fields, mixed
cell forms, invalid UTF-8/surrogates, trailing JSON and form-like scalar type
coercion are rejected. Input cannot name a Go class, factory or executable codec.

Defaults/hard limits are 32 MiB stream bytes, 10,000 records and 1 MiB encoded
record. `Limits` can lower them (set a compatible record bound when lowering
the stream bound). Constructor limits are 64 profiles, 128 fields per schema
and eight primary-key fields. Structured values are limited to depth 32 and
65,536 visited nodes per record/value; JSON number text is at most 256 bytes,
with at most 128 significant decimal digits and a normalized scientific exponent
between -(2^31+128) and +(2^31+128). These bounds include the canonical spelling
on repeated encode/decode; zeros and insignificant trailing zeros do not consume
significant digits. Decimal precision is at most 1,000 digits.
Raw provider fields share a 1 MiB snapshot text/binary budget per row, including
policy-only values. These are fixture safety limits, not global ORM limits.

## Export completion

`Dump` uses one owned read-only repeatable-read transaction, sorted model names
and explicit primary-key ordering. Text key order follows the database's
collation. It reads ORM-compiled pages of at most 64 rows, validates complete
row-reader termination, then closes that reader before grant/writer callbacks.
This permits read-only same-transaction ACL queries and bounds memory without
holding the entire output. No count query is needed.

`DumpResult.Bytes` reports observed accepted writes; `Records` reports completed
record writes. Only nil error plus `Complete: true` means the stream completed.
Reader/close/authorization/writer/cancellation or transaction failures may leave
bytes already written, including syntactically incomplete JSON. Discard that
stream; never retry into the same writer. No error text is appended to data.
The library does not claim an arbitrary writer provides atomic publication.

## Import, dry-run and outcomes

`Load` validates the bounded input before opening its own durable transaction.
Same-alias ambient transactions are rejected; unrelated aliases are preserved.
The actual provider transaction must implement `db.ConstraintCheckTransaction`;
missing capability is refused before any scope, row query or write. There is
no best-effort fallback.

All input grants precede the first INSERT. A private `orm.Store` and plain
`MapRecord` values use `SaveOptions{Raw: true, ForceInsert: true}`: no instance
hooks, Save receivers, field defaults, validators or auto timestamps execute.
Application validation belongs in the explicit read-only import policy, and
database constraints still apply. Import profiles reject DB defaults to prevent
implicit omission behavior. Database triggers are not disabled.

After inserting all rows, `CheckConstraints` runs deferred checks/triggers and
leaves constraints immediate. Only then are complete stored values and scoped
membership verified and grants rechecked. A final all-row reread detects an
earlier row changed by a later callback. Database normalization that changes the
canonical declared value fails the transaction. Imported snapshots are separate
from mutable SQL argument buffers.

`DryRun` performs the same INSERT/check/grant/read sequence, then confirms
rollback. It is database validation, not a no-SQL preview. Clean rollback yields
`DryRun: true`, `Committed: false`; failed cleanup is never reported as success.
External effects in application callbacks or database triggers are outside the
transactional rollback guarantee and violate the intended read-only policy.

A directly observed successful Commit yields `Committed: true`, even if the
context is canceled afterward. A genuine failing after-commit callback returns
that committed result with `ErrCommittedCallback`; it does not undo the write.
An uncertain/panicking commit returns a zero result and `ErrOutcomeUnknown`.
Retain fixture identities and reconcile against the selected database before
retrying. A definite failed transaction returns zero result. Errors expose only
safe one-based record/registered-field locations, never fixture values, SQL or
provider diagnostics. This is not a general backup/restore or migration tool.
