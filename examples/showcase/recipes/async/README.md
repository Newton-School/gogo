# Async recipes

Run from the showcase root:

```sh
GOWORK=off go test -v ./recipes/async
```

These are executable consumer examples using the pinned public Async module.
Every broker, result, workflow and schedule store here is an explicitly chosen
`async/testing.Memory` simulator. It exists only in `_test.go`; the application
must configure its Redis adapter separately. No example starts infrastructure,
uses production credentials or silently substitutes in-memory execution.

| Example / test | Demonstrated behavior |
| --- | --- |
| `Example_signatures` | Typed registration, detached signatures, options, immutable input and explicit eager execution |
| `Example_canvas` | Chain forwarding, ordered group results, chord barrier/body, immutable successor, named parent-field binding, readiness and indexed result iteration |
| `Example_mapStarMapAndChunks` | Queued map, strict struct-field tuples, grouped chunks and ordered outputs |
| `Example_callbacksAndFailureOutcomes` | Success callback, safe typed errback, failure-propagating join and non-propagating member outcomes |
| `Example_retryAndDelayedDispatch` | Task progress, explicit bounded retry, preserved retry counter, countdown, expiry metadata and separate delayed dispatcher |
| `Example_revokeRestoreAndForget` | Pre-execution task/workflow cancellation, result restoration, readiness and payload removal |
| `Example_periodicSchedules` | Revisioned schedule creation, interval, Beat → intent relay → worker → result, disabling future occurrences, UTC crontab |
| `TestCanvasRejectsUnsupportedBindingsAndLimits` | Incompatible chord types, invalid chunk size, bounded maps and scoped producer denial |
| `TestPeriodicMisfirePolicies` | Skip, coalesce and bounded catch-up intent counts |

The harness drives each public runtime role explicitly and advances a fake clock
instead of sleeping. It stops after a bounded number of tasks/coordination turns;
result waits also have a context deadline. Queue dispatch is not task completion.

These examples are not proof of Redis durability, crash recovery, process
isolation, failover, autoscaling, global Beat leadership, distributed rate
limits, or complete nested-canvas combinations. Cancellation is cooperative;
these examples revoke before execution, not an already-running handler. Map
items run sequentially within one task, and a chunk retry can repeat item side
effects. Forgetting payloads does not erase replay tombstones. Producer publish
retry and handler task retry are different protocols; only handler retry is
demonstrated here. PostgreSQL business outbox atomicity and production access
policies belong in the configured application, not this simulator.
