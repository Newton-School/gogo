# Files, uploads and downloads

Model `FileField` and `ImageField` values describe storage keys. Actual upload validation, file persistence, metadata and authorized delivery are separate services.

## Choose the right layer

| Layer | Responsibility |
| --- | --- |
| Form/API upload field | Validate submitted file metadata, bytes and image bounds |
| `files.Storage` | Provider-neutral storage operations |
| Local storage | Confined application-owned filesystem root, opaque keys and bounded operations |
| File service | Coordinate storage objects, metadata, authorization and lifecycle |
| Download handler | Current access checks, conditional/range delivery and safe HTTP headers |

Do not give an HTTP client a server filesystem path. Do not interpret a submitted path as a storage key. Validate the actual upload stream rather than trusting only filename, extension or declared Content-Type.

## Local storage

Construct local storage with an explicitly owned root. Saved objects use the storage contract's opaque identity and publication behavior. Close returned readers and the storage owner at the appropriate lifecycle boundaries.

Direct public URLs are not supported by the local adapter. Use the authorized file service and download handler. The local adapter is designed for supported Linux/macOS filesystem-confinement behavior; inspect the platform contract before deployment elsewhere.

The [storage recipe](https://github.com/Newton-School/gogo/tree/master/examples/showcase/recipes/storage) saves/deletes a disposable local object and demonstrates the public-URL refusal. It is not a cloud storage example.

## Metadata and authorization

Register the file service's metadata schema/migrations explicitly and connect its storage provider and policy. Authorize reads, uploads, attachment/replacement and deletion separately. A signed download URL is not permission to bypass current grants when the service requires them.

Storage publication and database metadata are not one atomic transaction. Preserve pending/failed outcomes and follow the service's recovery rules. Do not hide file deletion inside an ordinary ORM save callback where ownership and transaction boundaries become unclear.

## HTTP downloads

`files.NewDownloadHandler` provides the authorized download surface with bounded reads, validators/range behavior and safe content disposition. Configure its service and options explicitly. Test missing, denied, expired, changed and partially streamed objects.

## Limits

Direct cloud uploads, a full set of cloud providers and complete file/image Admin editing are not supplied by this alpha. A field specimen containing an example file key does not prove an object exists. See the local storage, service and download technical guides for exact supported operations and recovery semantics.
