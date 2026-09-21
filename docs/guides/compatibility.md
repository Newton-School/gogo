# Alpha limits and upgrading

These docs describe **v1.0.0-alpha.1** and source-backed examples. This is not stable 1.0, full Django parity, full Celery parity or certification for arbitrary production workloads.

## Known boundaries

| Area | Boundary |
| --- | --- |
| Model fields | All 40 kinds are declared; advanced extension/GIS kinds are not universally supported through persistence, forms, API and Admin |
| Forms | 29 kinds and explicit widgets; no ClearableFileInput or SelectDateWidget |
| Generic model HTML views | Supported scalar projections only; read each view's relation/file/custom-field restrictions |
| APIs | Explicit resource/write policies; no automatic nested persistence, reverse cursor navigation or universal browsable API |
| Admin | Explicit registrations and supported combinations, not every Django ModelAdmin hook/widget |
| Authentication | No built-in JWT/OIDC provider, complete legacy hasher catalog or encrypted durable reset-mail subsystem |
| Sessions | PostgreSQL and Redis stores; signed-cookie/cached-db names in settings are not complete backend implementations |
| Fixtures | JSON/JSONL scalar exports and non-auto-key insert-only imports; no relation/natural-key/upsert/asset transfer |
| Async | Repeated delivery is possible; no exactly-once external effects or Python/Celery wire compatibility |
| Scheduler | Per-schedule fencing; no completed database-wide leader lifecycle |
| Operations | No bundled Async dashboard or universal production deployment/restore certification |
| Connectors | PostgreSQL and Redis first; other backends need actual implementations |
| Files | Explicit local provider/service/downloads; no direct cloud upload ecosystem |
| Runtime modes | Command separation exists; environment-driven named modes remain proposed |
| Telemetry | Explicit instrumentation; no automatic full-stack coverage |

Constructor presence, schema validation, compile-only wiring, unit tests, real-provider integration and browser verification are different evidence levels. Consult the showcase coverage map and the feature's technical guide when choosing a capability.

## Version pinning

All six public module dependency versions use `v1.0.0-alpha.1`. Git tags for submodules have directory prefixes, but `go get` still receives the plain version:

```sh
go get github.com/Newton-School/gogo/admin@v1.0.0-alpha.1
```

Do not use `@admin/v1.0.0-alpha.1` as the module version. Avoid `@latest` when you intend this alpha: the earlier stable line is not the same implementation.

## Migrating from v0.x

The repository was rebuilt after v0.11.0. Old flat imports and queue APIs are not source-compatible. There is no automatic old-data or migration-history converter.

Create a separate client, port code/configuration explicitly, and rehearse any data migration and rollback against a backup. Do not point new migrations at an old production schema and assume they constitute an upgrade path.

## Documentation scope

The public framework source matched the alpha tag when these docs were authored. The standalone showcase and documentation were added after that immutable release commit. Source links identify the checkout used to build the site; newly generated documentation is not a new module release.

Review the boundaries above and the [Redis upgrade instructions](connectors.md#upgrade-from-prefixed-keys) before adopting or upgrading the alpha. Planned features are not implemented capabilities; rely on the documented behavior and its verification results.
