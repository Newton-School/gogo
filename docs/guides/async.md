# Tasks and workers

Async runs typed work independently of an HTTP request. Producers submit tasks; separate workers execute registered handlers. Redis persistence and recovery require the separately installed adapter and explicit configuration.

## Install

```sh
go get github.com/Newton-School/gogo/async@v1.0.0-alpha.2
go get github.com/Newton-School/gogo/connectors/redis@v1.0.0-alpha.2
go get github.com/Newton-School/gogo/async/redis@v1.0.0-alpha.2
```

## Declare typed tasks

**1. Set queue and execution limits.** This pure demo rejects tenant-scoped work. Real tenant tasks need current producer and worker authorization.

{{snippet examples/showcase/recipes/async/short_examples_test.go task-options}}

**2. Register a typed handler.** Both producer and worker must register the same name, version and payload types.

{{snippet examples/showcase/recipes/async/short_examples_test.go task-register}}

[Task options](options-async-taskoptions.md) · [Retries](options-async-retrypolicy.md) · [Dispatch options](options-async-dispatchoptions.md) · [Client](options-async-clientconfig.md) · [Worker](options-async-worker.md)

<details>
<summary>Complete showcase task file with group, chain and chord construction</summary>

{{code examples/showcase/apps/catalog/tasks.go}}

</details>

Names and versions are part of the dispatch contract. Producers and workers must agree on payload schemas and task identities. Register definitions before the registry freezes. Do not send executable code or credentials as task payloads.

## Construct the runtime

Follow [Wire a queue and worker](async-wiring.md) for the complete resource factory, management factories, producer command, and separate terminal commands. Installing Async and declaring a task are not enough to start consumption.

Create a `Client` with explicit registry, broker, results, workflows and allowed queues. Create a `Worker` with its queue selection, identity, concurrency and owned providers. For durable workflows/retries/delays, also configure the matching relay and delayed dispatcher; those services are not implicit goroutines.

`async/management.Commands` registers only the factories you provide. The [Async setup](async-wiring.md) includes complete wiring for Redis-backed workers, relay and delayed dispatch.

```sh
go run manage.go worker --concurrency 4 --queues showcase
```

This works in a project that registers that worker factory and queue. Changing a queue flag cannot register a missing task definition or grant permission.

## Submit and await

Call `Task.Delay(ctx, client, input, options...)`. A nil error confirms admission, not execution. The result handle's `Get(ctx)` waits for the result with the supplied context. Restore a handle by ID when you need to inspect later, subject to scope and retention.

Using `double` above, a [wired client and worker](async-wiring.md), and a deadline-bound `ctx`:

{{snippet examples/showcase/recipes/async/short_examples_test.go task-delay}}

Wait from the producer side after a worker starts consuming the `default` queue:

{{snippet examples/showcase/recipes/async/short_examples_test.go task-result}}

Options include queue, stable ID, priority, ETA/countdown, expiry, scope, headers and stamps. Keep allowed routes and queues explicit. A scope string is an input to policy, not a grant.

Do not synchronously wait for child work inside a worker. Use [workflows](workflows.md) or callbacks; worker-context joins are deliberately refused to avoid pool deadlocks.

## Execution and failure

Goroutine workers use cooperative cancellation. Task handlers and callbacks must honor context. Hard execution limits require the explicitly configured subprocess executor and registered child entrypoint; that is not a security sandbox for hostile code.

Delivery can happen more than once after failures. Idempotency for external side effects belongs to your application. A result fence prevents stale result writes, but cannot undo a payment/email/provider request already made.

## Core-only dispatch contract

`core/tasks.Dispatcher` lets application components depend on a small provider-neutral enqueue/result interface. `async.NewCoreDispatcher` adapts an explicitly configured Async client. Core does not acquire a broker dependency, start a worker or fall back to inline execution if no provider is configured.

Read [Scheduling, retries and recovery](scheduling.md) before treating a queue integration as operationally complete.
