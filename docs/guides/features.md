# Feature guide: find what you want to build

Use this as the product map. Each row leads to a human guide, then to detailed contracts and exact Go signatures. **Core is required; Admin and Async are separate modules.** Installing a module does not register its routes, migrations, resources, or workers.

Start with [installation](installation.md) → [first project](quickstart.md) → [running](running.md) → [database-backed API](tutorial-api.md) → [Docker](docker.md). Or [run the complete example](showcase.md) before reading implementation details.

## Projects and configuration

| Feature | What to use / where to learn it |
| --- | --- |
| Project generator, central `manage.go`, app scaffold | [Project tutorial](quickstart.md), [folder structure](structure.md) |
| App registration, dependencies, startup, shutdown | [Projects and applications](structure.md) |
| Typed settings, dotenv precedence, secrets, defaults, required resources | [Configuration](configuration.md), [all settings](settings.md) |
| Built-in CLI commands, custom commands, check registry | [Management commands](commands.md) |
| Trusted compiler-backed Go scripts and project resource bootstrap | [Runscript](runscript.md) |
| Development reload, compiled binary, server/worker roles | [Run and build](running.md) |
| Independent modules and versions | [Introduction](index.md), [compatibility](compatibility.md) |

## Models and databases

| Feature | Guide |
| --- | --- |
| Go structs, schema declarations, naming, model registry, validation | [Models](models.md) |
| All 40 field kinds, choices, defaults, nullability, constraints and indexes | [Field catalog](model-fields.md) |
| Auto/big/small identities, UUID and composite identities | [Field catalog](model-fields.md), [model technical guides](models.md#in-this-feature) |
| Text, numbers, exact decimals, booleans, dates, times, durations | [Field catalog](model-fields.md) |
| JSON, binary, files/images, arrays, ranges, HStore, search/GIS descriptors | [Field catalog and per-kind limitations](model-fields.md) |
| Foreign keys, one-to-one, many-to-many, through models, reverse accessors | [Relationships](relations.md) |
| Cascade, protect, restrict, set-null/default, deletion graphs | [Deletion policies](relations.md#delete-policies) |
| Filter/exclude, typed references, lookups, ordering, projections, iteration | [Queries](queries.md) |
| Save, explicit clean, bulk operations, conflicts, get/update-or-create | [Querying and saving](queries.md) |
| Eager loading, expressions, aggregates, windows, subqueries, row locks | [Query technical guides](queries.md#in-this-feature) |
| Transactions, savepoints, after-commit, aliases and database routing | [Transactions](transactions.md) |
| Generated migration source, dependencies, plans, apply/reverse, introspection | [Migrations](migrations.md) |
| JSON/JSONL data export/import and transfer limitations | [Fixtures](fixtures.md) |
| PostgreSQL schema/SQL capabilities and future backend interfaces | [Connectors](connectors.md) |

## HTTP and APIs

| Feature | Guide |
| --- | --- |
| URL converters, include/namespace, reversal, allowed methods, fallbacks | [Routing](routing.md) |
| Requests, response helpers, errors, middleware, generic HTML views | [Views](views.md) |
| Streaming responses and WebSockets | [Views and transport guides](views.md#in-this-feature) |
| Serializer fields, required/null/partial input, validation, representation | [REST APIs](api.md) |
| Scoped read resources, list/detail, filtering/search, conditional responses | [Working API tutorial](tutorial-api.md), [REST APIs](api.md) |
| Create, PUT/PATCH, delete, write authorization and atomic audit | [API writes](api-writes.md) |
| Idempotency keys, replay authorization, unknown commit outcomes | [Write receipts](api-writes.md#retries-and-idempotency) |
| Page number, limit/offset, forward cursors and throttling | [Pagination and rate limits](pagination.md) |
| Route metadata, OpenAPI and schema command | [REST APIs](api.md#transport-filtering-and-schema) |

## Forms and rendered pages

| Feature | Guide |
| --- | --- |
| All 29 form kinds; required input, validators, choices, errors | [Forms](forms.md) |
| Inputs, selects, radio groups, file controls, multi/split widgets | [Widgets](forms.md#widgets) |
| Model forms, explicit field allowlists and persistence | [Model forms](forms.md#model-forms) |
| Bounded formsets, existing IDs, ordering and deletion | [Formsets](forms.md#formsets) |
| Template loaders, context, inheritance, includes, escaping, tags/filters | [Templates](templates.md), [vocabulary](template-vocabulary.md) |
| Static sources, find/collect, fingerprints, manifests, template URLs | [Static assets](static.md) |
| Language negotiation, catalogs, plurals, timezones and localized dates | [Internationalization](i18n.md) |
| Humanized numbers, ordinals and temporal presentation | [Humanize](i18n.md#human-readable-values) |

## Accounts and request security

| Feature | Guide |
| --- | --- |
| Users, groups, model/object policies, password hashes | [Authentication](auth.md) |
| Login/logout, password change/reset and session identity | [Account views](auth.md), [complete Admin wiring](admin-wiring.md) |
| Scoped API tokens, expiry, revocation | [Authentication technical guides](auth.md#in-this-feature) |
| PostgreSQL/Redis sessions, rotation, cookies, flash messages | [Sessions and messages](sessions.md) |
| CSRF, purpose-bound signatures, key rotation, host/proxy/header policy | [Security](security.md) |
| Tenant/owner/publication predicates before counting or loading | [Query visibility](queries.md#visibility-belongs-in-the-query), [API scope](tutorial-api.md#2-declare-the-resource-and-row-scope) |

## Admin — optional module

| Feature | Guide |
| --- | --- |
| Installation, database/auth/session integration, login and middleware | [Set up a working Admin](admin-wiring.md) |
| Model registration and complete option map | [Admin](admin.md) |
| Columns, links, filters, search/help, sorting, pagination, empty values | [Lists, forms and actions](admin-customization.md) |
| Editable lists, fieldsets, read-only fields, form overrides | [Customization](admin-customization.md) |
| Inlines, autocomplete, raw IDs and scoped relation selection | [Customization](admin-customization.md), [relation guide](detail-admin-relation-select.md) |
| Prepopulated fields and top-of-form save controls | [Customization](admin-customization.md) |
| Actions, confirmation, deletion and history | [Admin security and writes](admin.md#security-and-writes) |
| Domain-backed user/group administration | [Accounts](detail-admin-accounts.md) |
| Authorized staff model/template documentation | [Staff docs](admindocs.md) |

## Async — optional module

| Feature | Guide |
| --- | --- |
| Typed task registration, versions, dispatch options, async results | [Tasks and workers](async.md) |
| Redis broker/results/workflows, worker commands and separate processes | [Wire a working queue](async-wiring.md) |
| Signatures, immutable signatures, parent inputs, callbacks/errbacks | [Workflows](workflows.md) |
| Chains, groups, chords, map/starmap/chunks | [Workflows](workflows.md), [executable recipes](async-recipes.md) |
| ETA/countdown, retries/backoff and publication retry | [Scheduling and recovery](scheduling.md) |
| Periodic intervals, cron, calendars, misfires and Beat | [Schedules](scheduling.md#periodic-schedules), [recipes](async-recipes.md) |
| Database outbox, committed intents, relay and delayed dispatch | [Scheduling and recovery](scheduling.md) |
| Cooperative/subprocess execution, limits, repeated delivery | [Task execution](async.md#execution-and-failure) |
| Join/iterate, result retention/forget, cancellation and revocation | [Workflow results](workflows.md#results-and-cancellation) |
| Controls, worker presence, events, diagnostics and reconciliation | [Control and monitoring](scheduling.md#control-and-monitoring) |
| Provider-neutral Core dispatch and test simulator | [Core dispatch](async.md#core-only-dispatch-contract), [testing](testing.md) |

## Application services

| Feature | Guide |
| --- | --- |
| Keyed cache, TTL, read-through, failure policy, invalidation | [Caching](cache.md) |
| Mail composition, attachments, SMTP and test/development backends | [Email](mail.md) |
| Private local files, upload services, authorized/signed downloads | [Files](files.md) |
| Typed local signals, receiver ordering, robust delivery | [Signals](signals.md) |
| Content types and permitted generic references | [Contrib](contrib.md), [content types API](api-core-contrib-contenttypes.md) |
| Site/domain resolution, redirects and flatpages | [Contrib](contrib.md) |
| Public sitemap pages/indexes and RSS/Atom feeds | [Contrib](contrib.md) |

## Testing, connectors and operations

| Feature | Guide |
| --- | --- |
| Unit/handler tests, race tests, isolated database/Redis checks | [Testing](testing.md) |
| Runnable, version-pinned example and feature/field coverage | [Showcase](showcase.md) |
| PostgreSQL, Redis roles, database/TLS/persistence rules, extension contracts | [Connectors](connectors.md) |
| Binary/image build, separate server/worker/migration processes, shutdown | [Deployment](deployment.md) |
| Liveness, startup/readiness, dependency probes | [Health checks](observability.md) |
| Explicit telemetry pipeline and safe console export | [Telemetry](observability.md#telemetry-pipeline) |
| Configuration, connection, migration, HTTP and worker failures | [Troubleshooting](troubleshooting.md) |
| All public Go packages, types, methods and settings | [API reference](packages.md), [settings](settings.md) |
| Unsupported combinations and v0.x migration boundaries | [Compatibility](compatibility.md) |

The catalog describes available APIs and documented boundaries, not complete Django/Celery parity. The [limitations table](compatibility.md#known-boundaries) identifies features that are only declared, partially supported, or not implemented.
