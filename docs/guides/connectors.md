# PostgreSQL, Redis and extension contracts

Core describes the contracts; connectors execute them against external systems. PostgreSQL and Redis are the initial supported external backends. Admin and Async remain independently installable.

The [PostgreSQL configuration](options-connectors-postgres-config.md) and [Redis configuration](options-connectors-redis-config.md) pages explain every connector option, zero-value defaults, incompatible combinations and security requirements. Environment settings only affect these structs when your project wires them into its resource opener.

## Install the adapters you use

In your client project:

```sh
go get github.com/Newton-School/gogo/connectors/postgres@v1.0.0-alpha.1
go get github.com/Newton-School/gogo/connectors/redis@v1.0.0-alpha.1
```

`startproject` already includes PostgreSQL. Install Redis only if a selected service needs it. Use `async/redis` from the optional Async module for durable task infrastructure, not the Core Redis connector alone.

For complete opening/closing code, use [the showcase's connection factory](async-wiring.md) or the generated client's `config/connections.go`. These factories open the resources selected by the current command and return cleanup functions; they do not share a global pool across processes.

## PostgreSQL

Construct `postgres.Open(ctx, postgres.Config{...})` in a project-owned resource opener. Configure DSN, alias, search path, pool sizes, connect timeout, maximum connection lifetime and required extensions. Close the backend during the owner's shutdown.

The adapter provides ORM execution, transactions, schema editing/migrations, constraint checks, type mapping, introspection and a session store. Production configuration requires verified TLS and rejects insecure fallbacks.

Schema capability checks matter: a PostgreSQL type name or extension does not imply a complete Go codec, model form, API representation or Admin widget. Read the index/constraint/introspection technical guides before relying on advanced schema operations.

## Redis roles

| Role | Intended data |
| --- | --- |
| `CacheRole` | Disposable cached values |
| `SessionRole` | Session state |
| `TaskRole` | Task broker state |
| `ResultRole` | Task results and workflow coordination |
| `SchedulerRole` | Periodic scheduling state |

Open named, role-specific connections with `redis.Open`. Configuration supports a URL or explicit addresses, and explicit Sentinel/Cluster options where applicable; do not combine incompatible addressing modes. Role connections can point at different servers.

Development connections require loopback. Production requires authenticated TLS. Durable-role checks include supported Redis version, `noeviction` and the relevant persistence requirements; development relaxations are explicit, not silent fallbacks.

## Redis databases

Only the Redis URL is required in client environment settings:

```env
GOGO_REDIS_URL=redis://127.0.0.1:6379/1
```

The `/1` selects database 1; another application can use `/2`. Omitting the database selects 0. Server, worker and scheduler processes that share state must select the same database for that state. Connections with explicit addresses or Sentinel use `redis.Config.Database`; a nonzero value supplied alongside `URL` must agree with the URL. Invalid or conflicting database selections fail instead of falling back.

```go
connection, err := redis.Open(ctx, redis.Config{
    URL: settings.Secret("GOGO_REDIS_URL").Reveal(),
    Role: redis.SessionRole,
    Development: true, // loopback-only local example; production requires TLS/auth
})
if err != nil {
    return err
}
defer connection.Close()
```

This wiring fragment uses `redis` from `github.com/Newton-School/gogo/connectors/redis`, an existing context and loaded settings. It adds no application namespace. Internal key families remain: `cache:0:<hash>`, `session:{<hash>}`, `queue:{<hash>}:...` and partitioned task/workflow/schedule keys. `Cache.Clear` removes only its cache version in the selected database, not sessions, jobs or another database.

[Redis Cluster supports only database 0](https://redis.io/docs/latest/commands/select/); use a separate cluster for each application/environment. Separate logical databases on standalone Redis share memory, persistence and server policy and are not an authorization boundary.

### Upgrade from prefixed keys

This is an **unreleased, breaking checkout change** after `v1.0.0-alpha.1`. The published alpha still requires its namespace setting; installing that tag does not install this behavior. The checkout showcase uses the checkout modules so its URL-only configuration works before the next release.

Remove `GOGO_REDIS_NAMESPACE`, `redis.Config.Namespace` and calls to `Connection.Namespace()` when upgrading. Existing application-prefixed keys are neither read nor renamed/deleted automatically. Before switching an existing deployment, stop new task submissions, drain workers and resolve retained delayed/periodic/workflow state under the old version; then stop all old processes and deploy all roles together. Do not mix old and new workers. Plan for cold caches and fresh sessions; invalidate client session cookies when resetting session storage. Migrate durable state only with an application-specific, verified migration if draining is not possible. Do not blindly strip prefixes: queues and coordination records contain cross-key references. Old quarantine cursors are invalid after the change; start a new listing. Keep the old data until the cutover is verified. Selecting another database likewise does not move data.

## Cache/sessions versus Async

`connectors/redis` implements Core cache, sessions and rate limiting without importing Async. `async/redis` implements broker, results, workflows, schedules, presence, events and recovery for the optional Async package.

Do not use a disposable cache deployment for durable job state without meeting the durable role contract. Automatic client reconnect/retry behavior is not proof of safe replay of a partially acknowledged mutation.

## Add another backend later

Implement the public contract for the feature you need: `db.Backend`/schema and dialect capabilities for a database, cache/session stores for those services, or Async broker/result/workflow/scheduler interfaces for task infrastructure.

Advertise only capabilities you implement. Preserve context cancellation, authorization boundaries at the owning service, stable safe errors, ownership, idempotency and unknown-outcome semantics. Run conformance and real-backend tests for supported features; a compile-time interface match alone is insufficient.

There are no bundled MySQL/SQLite/cloud-storage connectors or Redis substitutes promised by this alpha. Connector-neutral interfaces make future implementations possible without making them already available.
