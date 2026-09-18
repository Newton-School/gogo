# Scheduling, retries and recovery

Accepting a task is only the first step. Durable operation also needs delay/retry delivery, workflow advancement, pending-message recovery, result retention and clear handling of uncertain outcomes.

## Runtime components

| Component | Responsibility |
| --- | --- |
| `Worker` | Claim deliveries, execute handlers, renew leases and commit outcomes |
| `IntentRelay` | Publish committed retry/workflow intents |
| `DelayedDispatcher` | Dispatch tasks whose ETA/countdown becomes due |
| `Beat` | Lease due periodic schedules and create occurrences/intents |
| `Outbox` | Couple database business commits to eventual task publication |
| Redis `Reconciler` | Compose bounded reclaim, relay, delayed work and cleanup |

Wire only the components your application needs, but do not omit the relay that your configured retry/workflow path depends on. Configure `beat` through its management factory; the showcase does not register a periodic Beat process by default.

## Retry behavior

`MaxRetries = 3` permits three retries after the initial attempt. Ordinary errors do not automatically retry unless `AutoRetryFor` selects them. Explicit retry defaults to a 180-second delay; exponential backoff is opt-in.

Retry state is committed before acknowledging the original delivery. Separate handler retry from producer publication retry: after uncertain admission, preserve the original acceptance identity/envelope and use the documented retry path. Reconstructing new arguments with a fresh ID can duplicate work.

## Periodic schedules

Use explicit timezones with interval or five-field cron rules. Intervals must be at least one millisecond. Cron search and rule size are bounded. Configure misfire behavior rather than allowing unbounded catch-up after downtime.

Run the interval, cron, disable and missed-occurrence examples:

```sh
cd examples/showcase
GOWORK=off go test -v ./recipes/async -run 'Example_periodicSchedules|TestPeriodicMisfirePolicies'
```

The [complete schedule recipe](async-recipes.md) constructs `async.Every(time.Minute)` and `async.Crontab("*/15 * * * *", "UTC")`, persists an explicit schedule, ticks Beat and drains the task path. The interval result is `42`; disabling a schedule prevents new occurrences but preserves an existing result. After ten missed minutes, the bounded test expects zero intents for `skip`, one for `coalesce`, and three for `catchup` with a limit of three.

This is a deterministic simulator example. For deployment you must provide a durable scheduler store and register/run the Beat command factory; calling a calendar constructor alone neither saves a schedule nor starts a process.

Custom calendars must return a strictly later instant, remain side-effect-free and return promptly. The callback has no context argument, so a noncooperative calculation cannot be forcibly stopped by cancellation.

> Redis fences individual schedules. Namespace-wide scheduler leadership is not implemented in this alpha. Do not document Beat as a complete leader-election subsystem.

## Database outbox

Register `OutboxMigration` explicitly. Call `Outbox.EnqueueOnCommit` inside `db.Atomic`, then run the configured outbox relay. Only committed rows are published. The database transaction and Redis acknowledgement are not one distributed transaction; relay recovery and idempotency remain necessary.

## Control and monitoring

Control APIs include scoped result/status inspection, revocation, worker/queue inspection, quarantined-message diagnostics and explicit removal of one diagnostic entry. Each write operation needs its own permission; inspection is not an implicit mutation grant.

Configure worker presence and controls explicitly for remote heartbeats and exact-instance graceful shutdown. An accepted shutdown reply does not prove that a process exited or that all task effects stopped. Preserve unknown requests for reconciliation rather than retargeting a newly restarted instance.

Events are optional, redacted and lossy. They can be duplicated, trimmed or missed; result state remains authoritative. There is no bundled Async monitoring dashboard in this release.

## Retention and maintenance

Result expiry, forget operations, replay tombstones, workflow pins and undelivered intents have different retention rules. Cleanup can return a partial report plus an error. Retain its cursor and retry according to the documented provider contract.

Quarantine metadata does not retain an executable message body. Inspection or diagnostic removal does not implement replay or bulk purge. Read the detailed Async guide for exact limits, grants and outcome classifications before exposing operational commands to staff.
