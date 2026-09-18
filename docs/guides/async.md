# Tasks and workers

Async runs typed work independently of an HTTP request. Producers submit tasks; separate workers execute registered handlers. Redis persistence and recovery require the separately installed adapter and explicit configuration.

## Install

```sh
go get github.com/Newton-School/gogo/async@v1.0.0-alpha.1
go get github.com/Newton-School/gogo/connectors/redis@v1.0.0-alpha.1
go get github.com/Newton-School/gogo/async/redis@v1.0.0-alpha.1
```

## Declare typed tasks

The member-by-member references cover [TaskOptions](options-async-taskoptions.md), [RetryPolicy](options-async-retrypolicy.md), [DispatchOptions](options-async-dispatchoptions.md), [ClientConfig](options-async-clientconfig.md) and [Worker](options-async-worker.md). They distinguish task registration defaults from individual dispatch overrides and process-level runtime settings.

The showcase's `Tasks` holder keeps typed task handles alongside the registry. Handlers receive a context, task metadata and typed input; they return typed output or an error.

{{code examples/showcase/apps/catalog/tasks.go}}

Names and versions are part of the dispatch contract. Producers and workers must agree on payload schemas and task identities. Register definitions before the registry freezes. Do not send executable code or credentials as task payloads.

## Construct the runtime

Follow [Wire a queue and worker](async-wiring.md) for the complete resource factory, management factories, producer command, and separate terminal commands. Installing Async and declaring a task are not enough to start consumption.

Create a `Client` with explicit registry, broker, results, workflows and allowed queues. Create a `Worker` with its queue selection, identity, concurrency and owned providers. For durable workflows/retries/delays, also configure the matching relay and delayed dispatcher; those services are not implicit goroutines.

`async/management.Commands` registers only the factories you provide. The showcase's [Async wiring](https://github.com/Newton-School/gogo/blob/master/examples/showcase/config/async.go) is a complete example with Redis-backed workers, relay and delayed dispatch.

```sh
go run manage.go worker --concurrency 4 --queues showcase
```

This works in a project that registers that worker factory and queue. Changing a queue flag cannot register a missing task definition or grant permission.

## Submit and await

Call `Task.Delay(ctx, client, input, options...)`. A nil error confirms admission, not execution. The result handle's `Get(ctx)` waits for the result with the supplied context. Restore a handle by ID when you need to inspect later, subject to scope and retention.

Options include queue, stable ID, priority, ETA/countdown, expiry, scope, headers and stamps. Keep allowed routes and queues explicit. A scope string is an input to policy, not a grant.

Do not synchronously wait for child work inside a worker. Use [workflows](workflows.md) or callbacks; worker-context joins are deliberately refused to avoid pool deadlocks.

## Execution and failure

Goroutine workers use cooperative cancellation. Task handlers and callbacks must honor context. Hard execution limits require the explicitly configured subprocess executor and registered child entrypoint; that is not a security sandbox for hostile code.

Delivery can happen more than once after failures. Idempotency for external side effects belongs to your application. A result fence prevents stale result writes, but cannot undo a payment/email/provider request already made.

## Core-only dispatch contract

`core/tasks.Dispatcher` lets application components depend on a small provider-neutral enqueue/result interface. `async.NewCoreDispatcher` adapts an explicitly configured Async client. Core does not acquire a broker dependency, start a worker or fall back to inline execution if no provider is configured.

Read [Scheduling, retries and recovery](scheduling.md) before treating a queue integration as operationally complete.
