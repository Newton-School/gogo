# Compatibility Policy

This policy defines what Gogo treats as stable for framework users and generated
projects. It applies to the public module `github.com/Newton-School/gogo`, the
`gogo` CLI, generated project templates, migration files, and documented
runtime integrations.

## Semantic Versioning

Gogo uses semantic versioning for release tags.

| Version change | Meaning |
| --- | --- |
| Patch, for example `v1.2.4` | Bug fixes, security fixes, documentation updates, and compatible behavior fixes. |
| Minor, for example `v1.3.0` | New features, new public APIs, new generated template capabilities, and deprecations. |
| Major, for example `v2.0.0` | Intentional breaking changes to public APIs, generated project contracts, migration semantics, or runtime behavior. |

Before `v1.0.0`, breaking changes may happen in minor releases, but every
breaking change must be listed in `CHANGELOG.md` and covered by upgrade notes.
Patch releases must stay compatible with the matching minor release.

After `v1.0.0`, public APIs do not break within a major version. Deprecated
APIs should remain available for at least one minor release unless a security
issue requires faster removal.

## Public API Stability

The public API is the set of exported identifiers in documented non-internal
packages under `github.com/Newton-School/gogo`. The generated list in
`docs/code/generated/public-packages.md` is the source of truth for public package
visibility.

Stable public surfaces include:

- Exported types, functions, constants, and variables in non-internal packages.
- CLI commands and flags documented in reference or operations docs.
- Environment variable names loaded by `conf.LoadFromEnv`.
- Generated project file purposes and package layout.
- Migration operation names, graph semantics, and migration loader behavior.
- Queue task envelope fields and serializer names documented for persistence or
  broker transport.
- Admin registration contracts and permission names.

Unstable implementation surfaces include:

- Anything under `internal/`.
- Unexported identifiers.
- Test helpers that are not in the public `testing` package.
- Generated comments, whitespace, and formatting that do not change behavior.
- Diagnostic output not documented as machine-readable.

Public APIs may add fields, methods, or options in minor releases when the
addition does not break existing callers. Removing, renaming, changing a type,
changing a default with user-visible behavior, or changing stored data formats
requires a major release after `v1.0.0`.

## Migration File Compatibility

Generated migration files are durable project artifacts. Once a migration has
been applied in any environment, do not edit it manually. Create a new migration
for follow-up changes.

Compatibility rules:

- New framework versions must load older generated migration files from the
  same major version.
- New framework versions must keep existing migration operation semantics unless
  a major version explicitly changes them.
- Generated migration `Operations` metadata is the source of truth for
  `migrate` and `sqlmigrate`; filename-based SQL rendering is compatibility
  fallback only.
- Migration graph ordering, dependency resolution, and applied-record tracking
  must remain deterministic.
- Removed or renamed migration operations must provide a compatibility adapter
  or a clear upgrade error with the affected migration name.
- Raw SQL migrations must declare reversibility expectations and must not rely
  on development-only database state.
- Squashed migrations must preserve dependencies needed by already-deployed
  projects.

Migration compatibility is validated with fixtures for older generated
migrations, applied migration records, graph loading, and SQL rendering.

## Generated Project Compatibility

Generated projects are expected to be owned by the application team after
creation. The generator must not overwrite existing project files unless the
user explicitly asks for that behavior.

Compatibility rules:

- Generated `go.mod` must continue to use `github.com/Newton-School/gogo`.
- Generated `.env.example` must stay synced with framework settings.
- Generated `.gitignore` must keep production-unsafe files out of version
  control.
- Generated settings packages must keep separate local, test, production, and
  base layers.
- Generated apps must keep Django-style app structure for models, migrations,
  admin, APIs, forms, permissions, serializers, services, tasks, tests, and
  routes.
- Template changes must be documented so existing projects can compare their
  local files with freshly generated output.

Changing generated project layout in a way that breaks existing imports,
settings, middleware order, migrations, or commands is a breaking change.

## Supported Go Versions

The framework currently targets Go `1.26.4`, matching the root `go.mod` and
generated project templates. CI and release builds use Go `1.26.4`.

Supported Go policy:

- Use Go `1.26.4` or newer for development, CI, and releases.
- Generated projects should keep their `go` directive aligned with the
  framework template unless an upgrade guide says otherwise.
- A future Go minor version can be added in a minor release after CI covers it.
- Dropping the current Go minor version is a breaking change after `v1.0.0`.

## Supported Databases

| Database | Minimum supported version | Status |
| --- | --- | --- |
| PostgreSQL | `17` | Production target and CI integration database. |
| SQLite | Embedded engine provided by `modernc.org/sqlite v1.53.0` | Supported for tests, local development, generated project smoke tests, and single-process deployments. |

PostgreSQL is the production reference database. PostgreSQL-specific contrib
features, full-text helpers, trigram helpers, PostGIS helpers, and advanced
index behavior require the PostgreSQL dialect and matching database extensions.

SQLite support is intentionally narrower. Projects must not assume SQLite
matches PostgreSQL locking, constraint, concurrency, extension, or type
behavior.

Adding a database dialect is a minor feature. Removing a dialect, changing SQL
generation in a backward-incompatible way, or changing migration behavior for a
supported database is a breaking change.

## Supported Brokers And Result Backends

| Integration | Minimum supported version | Status |
| --- | --- | --- |
| Redis broker | `8` | Supported queue broker and CI integration service. |
| RabbitMQ broker | `4` | Supported queue broker and CI integration service. |
| Redis result backend | `8` | Supported task result backend. |
| SQL result backend | Same as supported database | Supported task result backend. |

Queue message envelopes, task names, retry metadata, scheduling metadata, group
metadata, chain metadata, and chord metadata must stay readable across patch and
minor releases within the same major version.

Changing persisted queue envelope fields, serializer names, result backend keys,
or broker routing semantics is a breaking change unless the framework includes
a compatibility reader or a documented migration path.

## Compatibility Testing

Compatibility tests must cover:

- Older generated project files.
- Older migration files and applied migration records.
- Stored queue messages.
- Signed sessions and password hashes.
- Admin URLs and permission names.
- Settings files and environment keys.

When compatibility cannot be preserved, the framework must fail with a clear
upgrade error before data is modified.
