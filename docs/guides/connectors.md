# PostgreSQL, Redis and extension contracts

Core describes the contracts; connectors execute them against external systems. PostgreSQL and Redis are the initial supported external backends. Admin and Async remain independently installable.

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

Use a valid unique namespace for each application/environment. A namespace prevents accidental key collisions; Redis credentials/ACLs and application policy still protect trusted adapter access.

## Cache/sessions versus Async

`connectors/redis` implements Core cache, sessions and rate limiting without importing Async. `async/redis` implements broker, results, workflows, schedules, presence, events and recovery for the optional Async package.

Do not use a disposable cache deployment for durable job state without meeting the durable role contract. Automatic client reconnect/retry behavior is not proof of safe replay of a partially acknowledged mutation.

## Add another backend later

Implement the public contract for the feature you need: `db.Backend`/schema and dialect capabilities for a database, cache/session stores for those services, or Async broker/result/workflow/scheduler interfaces for task infrastructure.

Advertise only capabilities you implement. Preserve context cancellation, authorization boundaries at the owning service, stable safe errors, ownership, idempotency and unknown-outcome semantics. Run conformance and real-backend tests for supported features; a compile-time interface match alone is insufficient.

There are no bundled MySQL/SQLite/cloud-storage connectors or Redis substitutes promised by this alpha. Connector-neutral interfaces make future implementations possible without making them already available.
