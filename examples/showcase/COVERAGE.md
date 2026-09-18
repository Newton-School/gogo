# Showcase map

The sample consumes **v1.0.0-alpha.1** through public module imports with
`GOWORK=off`. No local replacement is used. Its demonstrations are not a
certification that the entire approved framework architecture is implemented.

## Application tree

```text
docker compose up --build -d --wait             optional local launcher
├── setup                                      preserve private random credentials
├── PostgreSQL / Redis                         owned persistent volumes, loopback
├── initialize                                 check → migrate → seed; stop on error
└── web / worker                               start after successful initialization

manage.go
├── check / build / generate / makemigrations / migrate
├── seed / createadmin                         explicit operator writes
├── runserver                                 independent web process
│   ├── /api/v1/products/                      published records only
│   ├── /api/schema/                           public OpenAPI document
│   ├── /admin/                                persisted staff authority
│   │   ├── Products                           lists, forms, fieldsets, actions
│   │   ├── Product notes                      inline and relation handling
│   │   ├── Users / groups                     account administration
│   │   └── Field specimens                    scalar model field examples
│   ├── /fields/                               every declared field/form kind
│   ├── /forms/                                validation-only playground
│   ├── /cache-demo/                           Redis, 15-second cached value
│   └── /health/live/ and /health/ready/        process / dependency checks
├── worker                                    Redis task consumer + relays
└── demoasync task|delayed|chain|group|chord     submit and await separate worker
```

App code is under `apps/catalog/` and `apps/fieldlab/`. Configuration, explicit
connections, installed apps and HTTP composition are under `config/`.
Migrations never run as a web-startup side effect. Seeding never resets existing
credentials. The single-organization staff policy is not a multi-tenant demo.
Docker performs migrations in its separate initialization service, not in either
runtime entrypoint. Admin creation remains an explicit operator command.

## Fields and widgets

`go run manage.go fields` emits the machine-readable inventory; `/fields/`
renders it for browsing. Tests under `apps/fieldlab/` enumerate the 40 declared
model kinds (31 named constructors plus nine `NewField` kinds), all 29 form
kinds, and 21 widget configurations. Each row states its evidence and limits.

- Scalar model cases include valid/invalid cleaning, schema validation and
  PostgreSQL type mapping. The Specimen table contains the ordinary scalar
  kinds; small/standard auto IDs use separate tables. Relationship descriptors
  use a separate model and intermediary table.
- Array, generated, HStore, range, search vector, geometry/geography, raster and
  custom kinds are deliberately separated from ordinary CRUD. Extension/type
  mapping is not a database codec, GIS implementation or working Admin widget.
- Forms demonstrate normalization, validation, choice authorization, date/time,
  split fields and bounded file/image validation. Upload fixtures are tiny
  in-memory files; the browser form does not accept uploads or persist data.
- Binary, illustrative file/image keys and relationships are read-only in the
  field specimen Admin. A key string is not proof that a storage object exists.
- Unsupported ClearableFileInput and SelectDateWidget are listed as limitations,
  not silently replaced with equivalent-looking controls.

## Executable recipes

Run `GOWORK=off go test -v ./recipes/...`. These examples import the same pinned
release as the application, not repository-internal test helpers.

| Parent | Recipe directory | Demonstration boundary |
| --- | --- | --- |
| API | `recipes/api/` | All 23 serializer field constructors: binding and representation, bounded nested/list/dict inputs, exact numbers, uploads, model projections, read/write/hidden/source/partial/null rules, parser denial and explicit save policies. No database or nested-persistence claim. |
| Async | `recipes/async/` | Signature cloning, eager execution, chain/group/chord, immutable/parent binding, result iteration, map/starmap/chunks, callbacks/errbacks, retries/progress/ETA, revoke/restore/forget, periodic interval/cron and misfires. Explicit test-only Memory backend; the application's worker uses Redis. |
| HTTP | `recipes/websocket/` | Typed message declaration, origin/authorization callbacks and owned shutdown wiring; does not claim a real browser/socket exchange. |
| ORM | `recipes/orm_subquery/` | Compile-only scoped correlated subquery/exists composition; no hidden database connection. |
| ORM | `recipes/orm_routing/` | Compile-only primary/replica selection, consistency and transaction composition; no replication/failover claim. |
| Templates | `recipes/static/` | Static manifest dry run and template tag integration; deliberately no publication. |
| Files | `recipes/storage/` | Actual disposable local save/delete, opaque keys and unavailable public-URL denial on Linux/macOS. |
| Localization | `recipes/i18n/` | Accept-Language resolution, locale/timezone isolation and catalog pluralization. |
| Presentation | `recipes/humanize/` | Template filters and localized exact number formatting. |
| Contrib | `recipes/sitemaps/` | Explicit public scope, bounded sitemap pages/index and response validators. |
| Contrib | `recipes/syndication/` | Explicitly public Atom feed and named routing. |
| Operations | `recipes/health/` | Lifecycle-derived liveness/startup versus dependency-failed readiness. |
| Operations | `recipes/telemetry/` | Bounded explicit span pipeline and flush/close; no automatic instrumentation. |
| Services | `recipes/services/` | Multipart mail/test outbox/Bcc privacy/header denial; typed signals stop versus robust; random-key purpose-bound signing/tamper denial; bounded pagination. No actual email delivery. |

## Honest coverage boundary

This is a working demonstration project plus a field/API recipe laboratory, not
an example for every exported method or every combination of options. Some
implemented framework capabilities still have only their framework conformance
tests, not a sample workflow here: the complete ORM and migration operation
catalogs, fixture transfers, account-reset/delivery flows, storage metadata and
signed downloads, every Admin customization, and advanced Async outbox,
reconciliation/control/failover paths. Production restore, load and browser
accessibility evidence must not be inferred from unit/handler tests.

Consult each owning package's documentation and tests
before using a capability beyond the explicit demonstrations. Remaining
framework work is not papered over with placeholder handlers or fake providers.
