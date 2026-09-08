# Existing-owner private files

`files.Service` is an explicit lower-level bridge between private `Storage`,
`gogo_files` metadata, and one already-existing model File/Image field. The field
continues to contain a 32-character opaque **object key**, not a metadata UUID,
filename, URL, or multipart header. Nothing is registered or migrated implicitly.

Register `(&files.File{}).Schema()` with the application and explicitly apply
`files.Migrations()`. Its ten columns are `id`, `storage_alias`, `object_key`,
`owner_ref`, `state`, `content_type`, `bytes`, `checksum`, `created_at`, and
`finalized_at`. The migration enforces unique `(storage_alias, object_key)`,
nonnegative bytes, and the five approved states. There is no polymorphic foreign
key or database-enforced owner-reference/link consistency. Generic ORM writes
remain privileged operations that can create drift; the Service refuses drift
and does not repair it.

## Configuration and authority

`NewService(ServiceConfig)` takes a backend, model registry, one named Storage,
and explicit OwnerBindings. Each binding requires a concrete local File/Image
field, a root `orm.QueryScope`, an `Authorize` callback, and explicit
`PolicyFields`. The minimum selected schema is the primary key, bound field and
PolicyFields; include every local field your scope needs in that allowlist.
Unselected data is neither loaded nor passed to policy callbacks.

The initial provider slice supports scalar string/UUID/integer owner identities
(up to eight components) and string, boolean and integer policy values. It rejects
custom codecs, relations, generated fields, JSON, decimal, floating-point and
temporal policy data, proxy/inherited/unmanaged models, and cross-database owners.
Declarations are capped at 128 bindings, 32 policy fields per binding; the
canonical versioned owner reference is at most 4096 bytes. Alias/binding names
are bounded ASCII identifiers. UUIDs, content types and object keys are validated
without invoking input Stringer/marshaler methods.

Configuration, selected schemas, key tuples and policy map structures are frozen
before context/provider/reader/grant callbacks. Every grant receives fresh maps,
Previous metadata and individually detached timestamp locations. Backend,
Storage and authority implementations remain cooperative trusted ports: their
private state is not cloned and callers must not concurrently replace it.

Policies run with the owned transaction context. They must only read authority,
not mutate data, grants, or register mutation callbacks. Independent ACL changes
need application-owned locking/version coordination; selected-owner locks do not
lock an external ACL system. The preflight and Open transactions are genuinely
read-only, so database writes (including SELECT FOR UPDATE) there fail. The final
write transaction permits policies to lock independent ACL rows before checking.
An exact `ErrForbidden` is denial; mixed/unknown callback errors become safe
`ErrUnavailable`. No raw provider, SQL, path or callback panic text is returned.

## StoreValidated

Allocate and retain `NewUploadIdentity()` **before** starting an operation. Supply
the exact owner primary-key map, binding name, canonical ContentType, and reader.
`StoreValidated` assumes the bytes and MIME were already approved by the future
upload-handler boundary. It does not sniff MIME, parse multipart, validate an
original filename/extension, decode an image, or run field validators. Storage
still owns bounded streaming, checksums and exact no-overwrite publication.

The sequence is:

1. Reject malformed inputs and same-alias ambient transactions before reading bytes.
2. Read the scoped existing owner and authorize StoreFile in an owned read-only snapshot.
3. Publish the exact preallocated key through Storage.Save.
4. In a new owned durable transaction, lock the fresh scoped owner and any exact
   prior managed metadata, then authorize StoreFile and, when replacing, ReplaceFile.
5. Recheck the complete selected snapshot with the compiled scope, insert ready
   metadata, update only the bound field, and mark only the exact old ready
   metadata as deleting. Verify stored values before commit.

An unmanaged old key, foreign owner reference, conflicting state or absent old
metadata is a conflict, never permission to relabel/delete another file. The old
blob is retained. The current slice never performs orphan cleanup, deletion,
quarantine processing, retries, or state reconciliation.

| Observed outcome | Return contract |
| --- | --- |
| Confirmed storage failure before publication | Zero Info; StoreFailure.Published is false. |
| Published blob, failed bind/known rejected commit | Zero Info; Published is true. Keep identity and blob for privileged reconciliation. |
| Opaque Save panic/malformed response | PublicationUnknown is true; false Published does **not** prove absence. |
| Commit attempted but outcome unknown | Zero Info and ErrOutcomeUnknown. Do not blindly retry or delete the blob. |
| Commit returned nil | Detached verified Info; late cancellation alone cannot undo success. |
| Actual after-commit callback error/panic | Verified Info plus ErrCommittedCallback; StoreFailure.Committed is true. The write happened and must not be repeated. |

The commit marker belongs to the exact operation-owned transaction; a caller's
forged committed-error type does not establish an outcome. A confirmed-commit
callback failure is reported safely even though policies should not register
such callbacks. All diagnostic Info/error outcome objects are privileged library
data, not an HTTP response or permission to serve a URL.

Only explicitly named file storage-key uniqueness is normalized as
ErrMetadataConflict. A provider-specific concurrent UUID collision may be
ErrUnavailable after a definite rejection; there is no upsert or implicit
PostgreSQL constraint-name assumption.

## Open and revocation

`Open(ctx, metadataUUID)` requires ready metadata in the configured storage,
resolves its canonical reference through the frozen binding, applies root scope,
checks the owner's field still equals that exact key, and authorizes ReadFile
**before any storage access**. Non-ready, absent, out-of-scope or stale-link
metadata returns ErrNotFound without opening/statting a blob. Malformed metadata
and operational failures are not disguised as absence.

Open observes one owned repeatable-read read-only database snapshot. It checks
the grant and selected rows again after Storage.Open, closes any provisional
reader on failure, and releases the transaction before returning. The caller
owns the returned Reader and must Close it. Replacement or independently changed
ACLs after that observation can deny later opens but cannot retroactively revoke
already-started bytes. Repeatable read does not see later committed revocations.

This is not a download handler: response content disposition, media policies,
range/conditional requests, public representation, signed access and direct-upload
grants remain separate approved layers. Local storage's URL capability remains
unavailable and cannot bypass this authorization boundary. Destructive cleanup
requires a future explicit owner/lifecycle reconciliation contract; neither old
mtime nor an apparently absent metadata row proves an uncertain write is an orphan.
