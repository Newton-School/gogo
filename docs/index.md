# Build your backend with Gogo

Gogo is a Go backend framework for applications that need a consistent structure: models, database queries, HTTP views, APIs, forms, authentication, and management commands. Add Admin for staff tools and Async for background jobs when you need them.

Keep Go's types, interfaces, `context.Context`, standard HTTP handlers, and concurrency. Organize the application around one `manage.go` entrypoint and small, explicitly registered apps.

> You are reading the **v1.0.0-alpha.1** documentation. This is an early release, not a stable 1.0 or a promise of complete Django/Celery parity. Supported APIs, explicit integration requirements, and known limits are documented separately.

## Start building

| You want to… | Start here |
| --- | --- |
| Create a new Go application | [Your first project](quickstart.md) |
| See a working application immediately | [Run the showcase](showcase.md) |
| Understand the project layout | [Projects and applications](structure.md) |
| Know which environment variables to set | [Configuration](configuration.md) |
| Add an internal management interface | [Admin](admin.md) |
| Run work outside HTTP requests | [Tasks and workers](async.md) |
| Find an exact type, method or option | [Package reference](packages.md) |

## What is included?

| Feature family | What you can build |
| --- | --- |
| Application foundation | Project/app scaffolding, explicit app registration, typed settings, resource lifecycle, checks and custom management commands |
| Data | Model schemas and validation, typed ORM queries, relations, transactions, migrations, fixtures and database routing |
| HTTP and APIs | Named URL trees, HTTP/generic views, streaming and WebSockets, serializers, scoped API resources, OpenAPI, pagination and throttling |
| Presentation | Bound forms, model forms, formsets, widgets, templates, static assets, translation, timezone handling and human-readable formatting |
| Accounts and security | Users/groups, policy checks, password authentication, account views, API tokens, sessions, messages, CSRF and signed values |
| Admin | Registered model lists/forms, filters, search, editable lists, fieldsets, inlines, relation controls, actions, history and staff docs |
| Async | Typed tasks, queues, workers, results, retries, scheduling, chains/groups/chords, recovery, control and test helpers |
| Services | Cache, email, local file storage and authorized downloads, typed signals, sites, content types, redirects, flatpages, sitemaps and feeds |
| Operations | PostgreSQL/Redis connectors, process separation, health checks, explicit telemetry, verification and upgrade boundaries |

## Choose your modules

| Module | Install it for |
| --- | --- |
| `github.com/Newton-School/gogo` | Core framework and management CLI |
| `github.com/Newton-School/gogo/connectors/postgres` | PostgreSQL persistence; generated projects include it |
| `github.com/Newton-School/gogo/admin` | Optional staff Admin |
| `github.com/Newton-School/gogo/async` | Optional task execution and workflows |
| `github.com/Newton-School/gogo/connectors/redis` | Redis connections, caching and sessions |
| `github.com/Newton-School/gogo/async/redis` | Redis-backed Async broker, results and coordination |

Install every module at `v1.0.0-alpha.1`. Installing a package does not mount routes, create database tables, open connections or start workers. Your project's configuration does that explicitly.

## Read the docs in layers

Tutorials show the first working path. Feature guides explain what to register, how to call it, and what to handle on failure. Technical guides preserve the detailed contracts for advanced features. The generated Go reference lists the public declarations and links each one to its source.

Examples taken from the showcase use its `example.com/gogo-showcase` module and app names. Adapt those imports to your project; the showcase itself is ready to run unchanged. Small examples under `docs/examples/` are tested without external services. Neither set of examples certifies every possible combination of features.
