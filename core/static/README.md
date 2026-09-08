# Public static assets

`core/static` discovers explicitly public project/app assets, rewrites supported
CSS dependencies, fingerprints final bytes, and publishes a versioned local
manifest. It is independent of private uploaded-file storage. Collection does
not open database/Redis connections, start application `Ready` hooks, register
routes, or inspect arbitrary installed packages.

## Explicit sources and precedence

Construct a `static.Collector` with `static.New(static.Config{...})`. Each
`Source` has a public owner label and exactly one `Directory` or `FS`. Declare
project overrides first. Apps can call `static.Register(registry, appLabel,
sources...)` during their `Register` hook; `static.AppSources(registry,
installedApps)` returns those declarations in the framework's dependency order.
Append these after project sources. No global filesystem search is performed.

The first logical path wins. `Find(ctx, name)` returns every matching owner in
precedence order and marks the selected one, without reading file contents or
opening the destination. `Collect` reports shadowed matches too. Source lists
are copied at construction; explicitly supplied `fs.FS` providers remain trusted,
cooperative providers. Use `embed.FS`/`fs.Sub` for embedded app assets, or
`Directory` for a host filesystem. Passing `os.DirFS` does not create a sandbox.

Directory sources use Go's rooted filesystem descriptors. Logical paths are
UTF-8 slash paths, at most 2,048 bytes, without traversal, backslashes, control
characters, query/fragment delimiters, or ambiguous trailing dots/spaces. All
dot-prefixed path segments are ignored, and observed symlinks/nonregular files
are rejected. Local source roots and the destination cannot overlap, including
through observed symlinked ancestors. An opaque `FS` provider must enforce its
own source isolation. Sources and their ancestors must be controlled by trusted
deployment operators; hostile same-privilege writers, hard links, mount changes,
and network-filesystem semantics are not an OS sandbox. Collection is not a
snapshot of concurrently edited source trees.

## Fingerprinting and publication

`BaseURL` is required: either a local absolute slash-ending prefix such as
`/static/` or an explicit HTTP(S) CDN prefix. No network fetch occurs. Every final
asset is SHA-256 hashed. Versioned names are `original/directory/<full-hash>.ext`,
retaining the extension and directory, not the original basename. The manifest
maps exact original logical paths to these names, checksums, and byte sizes.

Supported CSS dependencies are processed dependency-first. `url(...)` accepts
quoted/unquoted values and CSS escapes; `@import` accepts a string or `url(...)`
and preserves its layer/supports/media tail. Tokenization preserves unrelated
comments, strings, and external/data/blob/protocol-relative URLs byte-for-byte.
Fragment-only references stay unchanged; a query-only reference targets the
same file and therefore fails the cycle check. Relative paths may traverse to a
parent within the static root. Root-relative paths are collected only below
the configured static URL prefix. Absolute HTTP(S) URLs matching the exact
configured CDN origin and path prefix are also resolved from local sources;
unrelated origins and paths remain untouched. Query/fragment suffixes survive
rewriting. No remote dependency is downloaded.

Missing local references, cycles, unsafe paths, and unsupported recognized
dependency syntax refuse publication. This is a bounded CSS subset, not a full
browser CSS parser: `image-set`/`-webkit-image-set` and source-map directives are
explicitly rejected; JavaScript imports, source maps, and complete Django
post-processing parity are not implemented. Use supported `url(...)` forms or
perform unsupported transformations in an explicit build step first.

`Collect(ctx, static.CollectOptions{DryRun: true})` performs the same discovery,
validation, CSS rewrite, hashing, and manifest construction, with no destination
writes. A destination is still required and checked for separation. Normal
collection requires its parent directory to exist; it creates only the configured
destination leaf and owned children. The initial publisher supports Linux/macOS
on the project's patched Go minimum, with local atomic rename/hard-link semantics;
other platforms return `ErrUnsupported` before publication.

All assets are complete, synced, and closed before publication. Existing hashed
files are verified and never overwritten; new files use exclusive staging and
no-overwrite hard links. After all assets and directories are ready, one manifest
rename publishes the new generation. Old hashes are retained for cached clients.
There is no destructive clear, orphan sweep, or automatic garbage collection.
Failure may leave safe unreferenced immutable files; only this operation's exact
temporary entries are cleaned up.

`Report.Published` becomes true only after manifest rename succeeds. A later
sync/cleanup error retains that true outcome and the prepared manifest: do not
claim rollback or blindly retry. Failures before observed publication return no
manifest result. Concurrent collectors can each publish a complete generation;
the last manifest rename wins. There is no distributed transaction, source-tree
snapshot, or guarantee across hostile/nonlocal filesystems.

## Bounds and errors

| Per-operation limit | Default | Maximum |
| --- | --- | --- |
| Sources | Explicit, nonempty | 128 |
| Directory entries (including shadowed/ignored) | 10,000 | 100,000 |
| One input/output file | 16 MiB | 256 MiB |
| Total input and final output, separately | 256 MiB | 1 GiB |
| CSS input/rewritten output | 2 MiB | 16 MiB |
| CSS references | 100,000 | 1,000,000 |

Directory/dependency depth is at most 64. Manifest JSON and command output are
bounded to 16 MiB. Limits are refusal boundaries, not truncation. Context
cancellation is checked around cooperative provider work; blocked filesystem or
provider calls are not interrupted with abandoned goroutines. Provider panic
text and physical source paths are omitted from normal error formatting. Error
causes remain available through `errors.Is`/`errors.As`; do not expose inspected
provider causes to clients.

## Manifest URLs, templates, and development

Use the returned manifest or `LoadManifest(ctx, destination, baseURL)` to create
an immutable lookup snapshot. `ReadManifest` also accepts a caller-owned reader.
Unknown versions, unknown/duplicate logical paths, invalid hashes and malformed
metadata fail closed. `Manifest.URL(name)` never falls back to an unhashed URL.
`Assets()` and `JSON()` return detached copies. Reload explicitly after deployment;
template rendering does no filesystem access.

Register `manifest.Tags()` through `templates.Config.Tags`, and allow `"static"`
in `Config.Libraries`. Both `{% static "css/app.css" %}` and the engine's existing
`{% static path as asset_url %}` syntax work. `{% get_static_prefix %}` returns the
configured prefix. Values are ordinary escaped strings, never trusted HTML.

`collector.DevHandler(settings.Bool("GOGO_DEBUG"))` must be mounted explicitly
under the configured path. False refuses construction. It serves only unhashed
public source files with GET/HEAD, bounded conditional requests, no directory
listing, `no-cache`, and `nosniff`. Transfer failures abort the response. It does
not serve private media or become the production asset server. Configure the
production web server/CDN to serve only the collected destination, with immutable
caching for hashed filenames and suitable manifest deployment/reload policy.

## Opt-in commands

Append `management.StaticCommands(resolve)` to the project's explicit commands.
The resolver builds the same Collector from project settings and app declarations.
Use `settings.String("GOGO_STATIC_ROOT")` for its destination. `collectstatic`
requires that existing configuration role, supports `--dry-run`, and emits one
bounded JSON result; `findstatic logical/path` does not require an output root.
Unknown flags/extra arguments fail before the resolver. Neither command opens
services or runs `Ready`. Errors after publication, including short output writes,
are reported as post-publication failures. Full Django command flags and generated
project registration are intentionally not implied by this opt-in API.
