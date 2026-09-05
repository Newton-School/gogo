# Gogo Async

Typed Go tasks, leased workers, durable chains/groups/chords, retry intents and periodic scheduling. Install the Redis adapter separately:

```sh
go get github.com/Newton-School/gogo/async
go get github.com/Newton-School/gogo/async/redis
```

Register tasks before freezing the registry. Producers call `Task.Delay` or `Client.ApplyCanvas`. Workers run `Worker.Run`; run `IntentRelay` alongside workers and `Beat` for periodic schedules. `DelayedDispatcher` processes scheduled work, and the Redis `Reconciler` combines reclaim, relay, delayed dispatch and bounded result cleanup. Backend roles can use separate Redis servers.

`max_retries=3` allows three retries after the initial execution. Ordinary handler errors do not retry unless `AutoRetryFor` is configured. Explicit `Retry` uses a 180-second delay; exponential backoff is opt-in. Durable retry state is committed before acknowledging the original stream delivery.

Tasks may execute more than once after a process failure. Use task IDs/idempotency keys to guard external effects. A terminal result fence prevents stale workers from overwriting the current outcome; it cannot undo an already-issued external request. Do not put credentials in task arguments, headers, progress, or event data.

For PostgreSQL transaction coupling, register `OutboxMigration` explicitly, then call `Outbox.EnqueueOnCommit` inside `db.Atomic`. The outbox relay publishes only committed rows. Queue acknowledgement and the business transaction are not one distributed transaction.

Task results and control calls enforce the configured scope policy. Goroutines use cooperative cancellation; hard deadlines require `ProcessExecutor` plus a client command that calls `Registry.ServeChild`. Use `async/testing` only when a process-local test simulator is intended.

`async/management.Commands` registers only explicitly supplied worker, beat, task/queue control and subprocess-child factories. The worker role supervises its configured relay, delayed dispatcher and outbox. Invalid flags fail before resources open. Status does not print arguments or result data; `tasks result` is an explicit separately scoped operation.

`TaskOptions.PerWorkerConcurrency` limits one task type within each worker; `TaskOptions.Rate` explicitly chooses a local or distributed budget. Distributed mode requires the configured `Worker.RateLimiter`. Rate waiting retains a bounded worker slot and recoverable broker reservation without consuming execution retries. It is not a separate durable rate scheduler.

Management shutdown respects `GOGO_SHUTDOWN_GRACE`. `ErrShutdownTimeout` means non-cooperative handlers may still run; it never means Go goroutines were killed. When management is called as a library, core resources may close while those handlers remain active. Use subprocess isolation plus an external process supervisor when forced termination is required, and keep external effects idempotent.

Compose nested workflows with `signature.Canvas()`, `canvas.Then(next)` and `Parallel(canvases...)`. Chains, groups and chords can be nested; the complete graph is validated before acceptance. Ordered collectors and dispatch claims persist together, so restarting a relay needs no original Go builder objects. Incompatible types/scopes are rejected before dispatch; failed prerequisites produce independent failed descendant records without running their handlers.

This implementation is under active conformance work. Replacement/ignore task controls, remote worker administration, autoscaling/recycling and some advanced calendar/retention features are not complete. Passing the included tests is not a claim of full Celery compatibility or production release readiness.
