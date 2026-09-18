# Wire a Redis queue and worker

Read [task declarations](async.md) first. A task definition is not a running queue. This chapter connects the definition to a broker, result store, worker process, and the retry/workflow services the worker needs.

## Install the three optional modules

```sh
go get github.com/Newton-School/gogo/async@v1.0.0-alpha.1
go get github.com/Newton-School/gogo/connectors/redis@v1.0.0-alpha.1
go get github.com/Newton-School/gogo/async/redis@v1.0.0-alpha.1
```

Set `GOGO_REDIS_URL` and `GOGO_REDIS_NAMESPACE` in the client. Select the `redis` resource for the commands that use it. Development Redis must be loopback; use [the showcase's verified Docker topology](showcase.md) or run Redis locally. Production roles require authenticated TLS and the documented persistence policy.

## Open role-specific connections

This is the showcase resource factory. For Async, focus on `TaskRole` and `ResultRole`; its cache/session roles serve other example features:

{{code examples/showcase/config/connections.go}}

Each process owns its connections and closes them during shutdown. The task broker connection and result/workflow connection can be different backends in a real deployment. Do not use an evicting, disposable cache for durable task state.

## Register runtime factories and a producer command

Save equivalent wiring in your project's `config/async.go`. This complete example uses the task holder from [Tasks and workers](async.md):

{{code examples/showcase/config/async.go}}

Append `connections.AsyncCommands()` to `Project.Commands` in `config/settings.go`. The returned worker runtime includes:

| Component | Why it is there |
| --- | --- |
| Worker | Consume `showcase`, execute registered tasks, commit outcomes |
| Intent relay | Publish committed retries and workflow successors |
| Delayed dispatcher | Make ETA/countdown tasks runnable when due |
| Client | Producer admission and result access |

The example does **not** register a Beat factory. Add one deliberately when adopting periodic schedules. A result connection used by schedules is not a running scheduler.

## Run two processes

With the showcase's native configuration, terminal one:

```sh
GOWORK=off go run manage.go worker --queues showcase --concurrency 2
```

Terminal two:

```sh
GOWORK=off go run manage.go demoasync task
GOWORK=off go run manage.go demoasync delayed
GOWORK=off go run manage.go demoasync group
GOWORK=off go run manage.go demoasync chain
GOWORK=off go run manage.go demoasync chord
```

For Docker, Compose already starts the worker; run `docker compose run --rm --no-deps web demoasync task`. The simple task accepts input 21 and returns 42. Admission prints an identity before completion prints the result. If the worker is stopped, accepted work does not become a completed result merely because the producer succeeded.

## Publish from your own handler or service

Once `tasks` and `client` are the initialized values above:

```go
result, err := tasks.Double.Delay(ctx, client, 21)
if err != nil {
    return err
}
taskID := result.Receipt.ID
```

Return the task ID to the caller through your API and query retained results through an authorized path. Do not wait synchronously inside a worker for work on the same pool. For an HTTP request, decide explicitly whether waiting fits the request deadline or the API should return 202.

Retries can repeat side effects. An order/payment/email task needs application-level idempotency; task result fencing alone does not prevent a provider call from happening twice.

Next: [workflow and schedule recipes](async-recipes.md), [recovery](scheduling.md), and [production process separation](deployment.md).
