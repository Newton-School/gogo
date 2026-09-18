# Chains, groups and chords

Workflows combine registered task signatures. They coordinate durable task state, not arbitrary goroutines and not Python/Celery wire messages.

## Choose the workflow shape

| Primitive | Behavior | Use case |
| --- | --- | --- |
| Signature | A reusable task identity, version, typed payload and dispatch options | Prepare work before submission |
| Chain | Execute signatures in order | Transform a result through several stages |
| Group | Dispatch independent children | Fan out independent work |
| Chord | Run a body after its header group completes according to the workflow contract | Aggregate a fan-out result |
| Map / starmap / chunks | Build bounded repeated work from input collections | Batch processing |
| Callback / errback | Explicit success/failure follow-up | Domain notifications or recovery work |

`async.Chord` returns a canvas **and an error**. Validate construction before submission. Parent-result binding is explicit through signature methods such as `FromParent`; an immutable signature does not implicitly receive a parent's output.

## Submit a canvas

The [task declaration example](async.md) contains a complete `Tasks.Canvas` method for group, chain and chord. Submit its result with `client.ApplyCanvas(ctx, canvas)`, then retain the returned workflow identity.

Run the complete checked examples from the framework checkout:

```sh
cd examples/showcase
GOWORK=off go test -v ./recipes/async -run 'Example_(canvas|mapStarMapAndChunks)$'
```

The [workflow recipe page](async-recipes.md) shows the exact source and expected outputs: chain `4`, group `[2,5]`, chord `7`, immutable successor `11`, bound parent field `7`, and chunk results `[[1,4],[9,16],[25]]`. It also explains the simulator helpers. For Redis execution, complete [queue and worker wiring](async-wiring.md) first; the simulator does not connect to Redis.

```text
Producer validates canvas and authorization
    ↓
Persist workflow state and dispatch intent
    ↓
Relay publishes eligible tasks
    ↓
Workers claim, execute and commit fenced outcomes
    ↓
Advance dependencies or record failure/retry
    ↓
Dispatch next stage / chord body when eligible
    ↓
Return authorized retained results
```

## Results and cancellation

`GroupResult` supports joining and iteration with explicit bounds, context and current authorization. Restore a group by workflow ID when needed. Results may be absent, pending, failed, expired or unavailable; those are different states.

A timeout while waiting does not revoke the workflow. Revocation is a separate authorized operation. Forgetting output is also separate from cancellation and can be constrained by active workflow references and retention rules.

## Keep side effects repeatable

Retries, worker failure and uncertain acknowledgements can repeat execution. Use stable business idempotency keys and transaction/outbox boundaries when a workflow calls external systems. Do not infer exactly-once behavior from a single terminal workflow result.

The showcase has executable chain/group/chord recipes and a Redis-backed runtime demo. That evidence does not cover every nested canvas, failure combination or distributed failover scenario.
