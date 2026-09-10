# Private local storage

`files.Storage` is a privileged blob interface. It does not authorize users,
create `gogo_files` records, bind owners, implement multipart uploads, or provide
download routes. Existing model File/Image values remain opaque strings. Do not
expose a Storage handle or a client-selected key as an authorization boundary.

Create an existing, dedicated directory with mode `0700`, then call
`files.NewLocal(files.LocalConfig{Directory: directory})`. The initial provider
supports Linux and macOS with the project's patched Go 1.26.8 minimum. Other
platforms return `ErrStorageCapabilityUnavailable`.

The application administrator owns the root and its ancestors. They must not be
writable by untrusted parties. The provider uses rooted descriptors, not lexical
path concatenation or `DirFS`; root renames do not retarget it. It rejects observed
symlink, directory, nonregular, and group/world-accessible object entries.
Filesystem writers with the application's OS privileges, hard links, mount
changes, hostile network filesystems, and compromised process code are outside
this boundary. Use a local filesystem with ordinary exclusive-create and atomic
hard-link semantics. It is not an OS sandbox or a multiprocess quota manager.

## Keys, publication, and completion

Call `files.NewKey()` in trusted server code and retain the returned key before
calling `Save`. Keys have exactly 32 lowercase hexadecimal characters. Original
filenames, absolute/relative paths, extensions, uppercase aliases, and trailing
slashes are rejected. There is no automatic collision renaming or overwrite.

`Save` streams directly to an exclusive `0600` temporary file using a 64 KiB
buffer. The default maximum is 10 MiB; `MaxBytes` can select another positive
limit. Empty blobs are supported. The stream is read through EOF, with at most
one byte beyond the limit to detect overflow. Invalid reader counts, no-progress
readers, cancellation, and reader errors fail before publication. Context/reader
panics become safe `ErrUnavailable`; panic text and physical filesystem paths
are not included in error formatting. An ordinary reader error's identity is
available through `errors.Is`/`errors.As`; applications must treat the underlying
provider error as sensitive when inspecting it.

After writing, the file is synced and closed. A hard link publishes the complete
object atomically without replacing an existing key. Concurrent attempts at the
same key have at most one successful publication; others receive `ErrCollision`.
This does not promise directory-fsync crash durability, a distributed transaction,
or that a later metadata transaction committed.

| Save result | Meaning |
| --- | --- |
| Zero result and error | No publication was observed by this operation. |
| `Published: true`, nil error | Complete blob published; owned temporary cleanup and descriptor completion succeeded. |
| `Published: true`, nonnil error | Publication happened, but subsequent cleanup/completion failed. Do not blindly retry or assume rollback. |

Published results include the exact key, byte count, lowercase SHA-256 checksum,
and UTC modification time. The checksum is computed while streaming; the local
provider does not persist a separate metadata record. Cleanup only removes the
current operation's exact temporary name, never an old or unrelated entry.
Process termination can leave a temporary file; automatic orphan reconciliation
is intentionally not part of this provider checkpoint.

`Delete` is idempotent for an absent key. Nil confirms observed unlink/absence,
not a directory sync. After a successful unlink it does not return a later close
or cancellation error that could misleadingly suggest rollback. There are no
recursive deletes. Storage changes must be coordinated with application metadata
by the later file service, not hidden in generic ORM Save hooks.

## Reads and lifecycle

`Open` returns a caller-owned `Read`/`Seek`/`Close` handle with no physical path.
Close it after use. Reads and seeks observe the context supplied to `Open`.
`Exists`, `Size`, and `ModifiedTime` distinguish absence from provider failure.
No stored key or file path is returned by `URL`: local direct URLs always return
`ErrStorageCapabilityUnavailable`. An authorized download service must recheck
current grants before using `Open`; use the explicitly configured
[file service](service.md) and [download handler](download.md) for that boundary.

`List` returns sorted keys after an optional exact key cursor. Its default page
size is 100 and maximum is 1000. It scans directory entries in bounded batches and
retains at most `Limit+1` keys, without exposing staging or unrelated names.
`Next` is the last returned key when another key was observed. Scans are weakly
consistent under concurrent insertion/deletion, not snapshot pagination. Memory
is bounded, but total directory-scan work is not; cancellation is checked between
batches. Invalid canonical object entries cause an error with no partial page.

Each operation captures its state and an independent root descriptor before any
context or reader callback. Copies of `Local` share Close admission state;
replacing an original handle cannot retarget an admitted operation. `Close`
prevents new operations, while admitted operations and previously opened readers
may finish. No caller callback runs under a storage mutex. Filesystem calls and
cooperative readers can outlive cancellation; no abandoned goroutine pretends to
interrupt a blocked call.

The future multipart upload layer owns its 1 MiB memory-to-temp spool threshold,
filename/type/image validation, owner authorization, quarantine, metadata commit,
and orphan cleanup. Those capabilities are not implied by `Storage.Save`.
