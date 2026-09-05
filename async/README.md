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

This implementation is under active conformance work. Nested canvases, replacement/ignore task controls, remote worker administration, autoscaling/recycling and some advanced calendar/retention features are not complete. Passing the included tests is not a claim of full Celery compatibility or production release readiness.
