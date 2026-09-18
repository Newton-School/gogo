# Try tasks, workflows, and schedules

The repository includes executable recipes pinned to the published alpha. Use them to learn input/output behavior before wiring Redis. These tests use an explicit simulator; they do not establish distributed failover behavior.

## Run the recipes

From the framework checkout:

```sh
cd examples/showcase
GOWORK=off go test -v ./recipes/async
```

The Go `Example_…` functions have checked `Output` comments. The helper functions and harness live in the same recipe package; copy the package if you want to run a recipe unchanged, not an isolated function that depends on missing helpers.

## Workflow inputs and outputs

{{code examples/showcase/recipes/async/canvas_test.go}}

Notice the difference between the shapes:

- A group receives independent inputs and exposes ordered child results.
- A normal chain successor receives the previous result; `ImmutableSignature` preserves its own original input.
- `FromParent("left")` places parent output into a named input field.
- A chord body receives header results and must accept the corresponding type.
- Map processes a bounded input collection inside one task. Chunks creates multiple such tasks; retrying a chunk can repeat earlier item effects.

The `newHarness`, `increment`, and checked-result helpers are defined in the [recipe harness](../../examples/showcase/recipes/async/harness_test.go). The [Redis example](async-wiring.md) demonstrates separate producer and worker processes with the same public canvas concepts.

## Periodic schedules

{{code examples/showcase/recipes/async/periodic_test.go}}

This recipe checks intervals, cron/timezone handling, misfires, and the periodic simulator. It is not a command that starts Beat in your application. Register an `async/management` Beat factory and its periodic runtime when you need a live scheduler; [Scheduling and recovery](scheduling.md) describes its required components and current leadership limitations.

## Failure and control behavior

The [lifecycle recipe](../../examples/showcase/recipes/async/lifecycle_test.go) exercises eager execution, retries, cancellation, callbacks, and result behavior. Read its assertions alongside the [runtime contract](detail-async-readme.md) before exposing administrative controls. Stop a waiting client and verify separately whether work is pending, running, completed, or revoked; timeout does not mean cancellation.
