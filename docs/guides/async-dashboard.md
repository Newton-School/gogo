# Dashboard

Inspect background work with server-rendered HTML, ordinary links, and GET filters. No JavaScript, frontend build, extra public package, or new environment variable is required. The dashboard is included in `v1.0.0-alpha.2`.

## Preview without services

From the repository root:

```sh
go run ./examples/showcase/recipes/dashboard
```

Open [the preview](http://127.0.0.1:5555/async/). It is labeled **Demo data**, binds only to loopback, and uses disposable memory fixtures. It does not connect to your application or authenticate production users. Stop it with Ctrl+C.

## Use the showcase

```sh
cd examples/showcase
docker compose up --build -d --wait
docker compose run --rm --no-deps web createadmin
```

Open [the dashboard](http://127.0.0.1:8000/async/). Anonymous visitors go to the existing Admin login. Access requires an authenticated, active, staff **superuser**; ordinary staff cannot inspect the operator-wide inventory. See [Example](showcase.md) for local credentials and setup.

Create observable work:

```sh
docker compose run --rm --no-deps web demoasync task
docker compose run --rm --no-deps web demoasync chord
docker compose run --rm --no-deps web demoasync schedule
```

The last command installs one minute-based schedule once; repeating it returns a revision conflict instead of overwriting the schedule. Compose runs separate worker and Beat processes. Native equivalents:

```sh
go run manage.go runserver  # terminal 1
go run manage.go worker     # terminal 2
go run manage.go beat       # terminal 3
go run manage.go demoasync schedule
```

## Pages

| Page | What you can inspect |
| --- | --- |
| Overview | Configured queues, queued/pending deliveries, quarantine counts |
| Tasks | Name/ID, state and queue filters; lifecycle timestamps, retries, deliveries, cancellation request, related IDs |
| Workers | Online/offline/lost/unknown presence, queues, concurrency, registered tasks, authorized active tasks |
| Queues | Per-queue broker observations; pending is not the number of running handlers |
| Beat schedules | Enabled flag, rule/timezone/DST, next due, misfire/overlap, lease expiry, last committed task |
| Beat instances | Last tick observation, expiry, online/error/offline/lost status |
| Workflows | Group/chain/chord/composed state and authorized task references |
| Events | Retained per-scope events, oldest first, with forward cursor navigation |

Use **Refresh** for current observations. Times are UTC. Task duration means creation-to-completion, including queue wait and retries—not measured handler duration. Arguments, results, progress JSON, headers, principals, lease tokens, and raw error messages are never rendered.

## Connect runtime stores

Use the same Redis connections and adapters as the worker, not separate copies of task state. Import `async` and alias `github.com/Newton-School/gogo/async/redis` as `asyncredis`:

```go
workers := &asyncredis.Workers{Connection: resultConnection}
beats := &asyncredis.Beats{Connection: resultConnection}
events := &asyncredis.Events{Connection: resultConnection}
catalog := &asyncredis.DashboardCatalog{
    Results: results, Workflows: workflows,
    Workers: workers, Schedules: schedules, Beats: beats,
}
```

Add the observation ports **before starting** runtime processes:

```go
worker.Presence = workers
worker.Events = events
beat.Monitor = beats
```

Use a unique worker ID and a unique Beat ID for each process lifetime. Set `ClientConfig.Events = events` on producers for enqueue events. Set `ClientConfig.Presence = workers` on the dashboard client. Event retention defaults to approximately 10,000 events per scope; `Events.MaxLength` accepts 1–1,000,000.

## Authorize access

Both `DashboardConfig.Authorize` and `ClientConfig.Authorize` are mandatory. Mount after identity/session middleware. This example uses Gogo's verified principal; an external authentication service can supply equivalent middleware and policy.

{{snippet docs/examples/dashboard_test.go dashboard-policy}}

Create a separate inspection client so its policy cannot accidentally change worker/producer authorization:

```go
client, err := async.NewClient(async.ClientConfig{
    Registry: registry, Broker: broker, Results: results,
    Workflows: workflows, Presence: workers,
    Queues: []string{"default"}, Authorize: policy,
})
if err != nil { return nil, err }
```

Inventory calls `inspect_dashboard` with empty scope and the page kind as ID: `overview`, `tasks`, `workers`, `queues`, `beat`, `schedulers`, `workflows`, or `events`. Individual records require `inspect` with their real scope/ID. Worker queues and active tasks are checked separately. Queue inspection uses the queue as scope and empty ID. Event reads retain the existing `inspect_events` and per-record inspection checks.

An explicit `async.ErrDenied` hides a list record or denies a direct detail request. Policy outages and malformed/backend responses yield **503**, with no partial records. Inventory access is operator-wide: do not treat record filtering as a tenant-isolated inventory protocol.

## Mount the handler

{{snippet docs/examples/dashboard_test.go dashboard-mount}}

Handle `err` as a startup error in your application factory. Do not use `http.StripPrefix`: the dashboard handles its own base path. The handler opens no listener and starts no workers. It can live in your web process or in a separately authenticated HTTP process using the same ports.

Enable the optional Events page:

```go
options.Events = events
options.EventScopes = []string{"", "billing"}
// Pass options to async.NewDashboard before starting the HTTP server.
```

Here `options` is your `async.DashboardConfig`. Explicit scope choices are configuration, not grants; the current caller still needs permission.

## Configuration

| Field | Behavior |
| --- | --- |
| `Client` | Required inspection client; configured queues are the displayed queue inventory, maximum 64 |
| `Catalog` | Required `DashboardCatalog`; Redis adapter discovers record IDs and reads schedules/Beat observations |
| `Authorize` | Required request gate; return `ErrDenied` to deny; panic or other failure becomes unavailable |
| `BasePath` | Defaults to `/async/`; root `/` or slash-delimited identifier segments, ending with `/` |
| `Title` | Defaults to `Gogo Async`; bounded, escaped text, maximum 80 bytes |
| `Events` | Optional reader; absent means the page explains that collection is not configured |
| `EventScopes` | Defaults to unscoped events; 1–64 distinct explicit scopes; first scope is selected initially |
| `PageSize` | Default 25; accepts 1–100 rows |
| `Timeout` | Default 5 seconds; accepts 1 millisecond–30 seconds; ports/policies must honor cancellation |

Only GET and HEAD are accepted. Responses use `no-store`, restrictive CSP, no-referrer and anti-framing headers. Keep the route private or behind your authenticated ingress; apply normal application request rate limits. Names, IDs and scopes are visible metadata and must not contain credentials.

## Discovery, retention, and limits

Redis discovery uses partitioned indexes, not `KEYS` or whole-database scans. Each request reads at most 128 inventory pages and 200 candidate records. Empty filtered pages may still have **Next page**. Inventory order is not chronological or a point-in-time snapshot; concurrent writes/expiry can change later pages. Exact task/workflow/schedule IDs and exact worker/Beat instance IDs can be looked up directly.

Task metadata is eligible for explicit cleanup seven days after completion or a later replay-protection deadline by default; active, pinned and pending-intent records remain protected. Payload visibility normally expires after 24 hours separately. These are not automatic Redis expiry guarantees: the showcase does not run task cleanup automatically, and workflow records currently have no automatic age-based cleanup. Configure maintenance and storage monitoring before sustained use.

Schedules retain their current records, not a complete occurrence history. Schedule discovery begins after the schedule's next save or committed tick following upgrade; existing disabled schedules remain reachable by exact ID until saved again. Worker indexes populate on the next heartbeat. Worker and Beat observations remain discoverable for 24 hours; expired heartbeats are **lost**, not proof the process died. `Beat.Monitor` writes a 30-second observation after a tick with a one-second best-effort budget; failures do not change task/schedule outcomes. Long ticks can appear lost until they finish. `Beat.OnMonitorError` can report sanitized observation failures.

An unavailable backend is not shown as zero work. Missing task records may be undispatched or no longer retained. A schedule's last committed task proves an intent was committed—not that publication or execution completed. Events are optional and lossy, not an audit trail. Workflow rows report retained workflow completion metadata, not a separately refreshed child-result snapshot.

This version does not expose revoke/retry/purge/shutdown buttons, schedule edits, complete attempt history, logs, or throughput charts. Use existing authorized control APIs separately. Dashboard reads never enqueue, acknowledge, cancel, or modify work.
