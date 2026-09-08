# Process and dependency health

Health is explicit: constructing a checker does not open connections, install
routes, close resources or start background polling. Each checker owns one
configured role, a fixed set of dependency probes, and a small readiness cache.

| Probe | Question | Dependency I/O |
|---|---|---|
| `KindLive` | Does the supplied process state consider this process live? | Never |
| `KindStartup` | Did required application startup finish? | Never |
| `KindReady` | Is the process live, started, accepting work and sufficiently healthy for its configured role? | Only selected dependencies |

```go
checker, err := health.New(health.Config{
    Role: "web",
    State: health.FromApplication(application),
    Dependencies: []health.Dependency{
        {ID: "database", Roles: []string{"web", "worker", "beat"}, Probe: database.Ping},
        {ID: "broker", Roles: []string{"worker", "beat"}, Probe: redisConnection.Ping},
    },
})
if err != nil { return err }

live, err := health.NewHandler(checker, health.HTTPOptions{Kind: health.KindLive})
if err != nil { return err }
startup, err := health.NewHandler(checker, health.HTTPOptions{Kind: health.KindStartup})
if err != nil { return err }
ready, err := health.NewHandler(checker, health.HTTPOptions{Kind: health.KindReady})
if err != nil { return err }

// Include these in the application's normal URL tree.
patterns := []urls.Route{
    urls.Path("/health/live", live, "health-live"),
    urls.Path("/health/startup", startup, "health-startup"),
    urls.Path("/health/ready", ready, "health-ready"),
}
```

`application`, `database` and `redisConnection` are application-owned instances.
PostgreSQL Backend and Redis Connection expose the same `Ping(context.Context)`
method shape; future providers can supply any compatible bounded probe. The
checker does not require a connector import or choose a provider for clients.
Configure only resources needed by the selected role; an empty dependency Roles
list selects every role, not no roles.

`FromApplication` reads the application's secret-free lifecycle snapshot.
Successful startup remains latched through drain and shutdown. Admission stop
or lifetime cancellation immediately prevents readiness; it does not erase
completed startup. Closed/failed application state prevents liveness. Resource
outages alone affect readiness, not the application lifecycle or liveness.

The State callback is trusted, read-only, in-process and nonblocking. It runs
without checker locks and must not perform I/O or call back into a health check.
Applications with a separate worker-admission or fatal-runtime state must compose
it explicitly. An HTTP handler responding does not establish that every other
goroutine is responsive, and no local code can answer while its entire process
is unresponsive. Orchestrator timeout/restart policy remains external.

## Readiness evaluation

```text
Fresh local state → reject not-started/dead/draining immediately
                 → use unexpired dependency report, or join/start one flight
                 → select only configured role's dependencies
                 → bounded admission → each probe's own timeout
                 → complete safe passed/failed/timeout/busy results
                 → required failure = not_ready
                 → only explicitly bypassable failures = degraded
                 → all pass = ready
                 → fresh local-state check before returning
```

Readiness requires `Live && Started && Ready && !Stopping`, even if a custom
state source supplies contradictory flags. Cached success cannot hide an
observed drain. Both success and failure are cached; cache expiry does not
trigger polling until another readiness request arrives.

A shared flight has a checker-owned context with no request values. Canceling
one waiter returns its context error without canceling other waiters or poisoning
the cache. Live/startup calls do not join readiness flights. Dependency callbacks
may run concurrently and must tolerate cancellation. Provider errors and panic
values are never formatted, retained in reports or returned to clients.

A timeout ends observation, not the callback itself. Capacity and per-dependency
ownership remain reserved until that callback actually exits. Later requests
cannot overlap another instance of that same stuck probe; they report `busy`,
or time out waiting for global capacity. Late results do not amend an already
returned/cached report. No unbounded retry, automatic pool close or goroutine
termination is attempted.

`Optional: true` alone still fails readiness on failure. Only
`Optional: true, AllowDegraded: true` explicitly tolerates that dependency and
reports `degraded` (HTTP 200). This does not enable bypass in the actual cache,
database or business operation: configure compatible application behavior first.
Do not mark a critical broker/database bypassable just to make a probe green.

## Public and authorized diagnostics

`checker.Check(ctx, kind)` returns a detached `Report`. Dependency failures are
status results, not raw provider errors; invalid configuration, canceled waiters
and failed local observations use stable errors with no partial report.

Public handlers serialize only `status` and an optional trusted request ID from
Core HTTP middleware. They never expose role, dependency IDs, endpoints or
provider errors. Successful statuses are live, started, ready and degraded;
unhealthy statuses return 503. GET/HEAD share checks and content length, with no
HEAD body. Other methods return 405; request bodies are rejected without reading
them. Responses are JSON, `private, no-store` and `nosniff`; partial network writes
abort instead of being reported as successful responses.

Detailed diagnostics require a separately configured handler:

```go
diagnostics, err := health.NewHandler(checker, health.HTTPOptions{
    Kind: health.KindReady,
    Diagnostics: true,
    Authorize: authorizeHealthDiagnostics,
})
```

Mount this explicitly behind current application/service authorization. The
required read-only policy runs before evaluating a report and again before
emitting it. Exact `health.ErrForbidden` returns 403; wrapped/mixed errors,
panics and cancellation return safe 503. A final local-state check follows the
last grant. Query parameters cannot enable diagnostics, alter the configured
role or select extra dependencies. A cached report never caches an access grant.

Diagnostics contain only configured safe IDs, status codes, optional/bypass
flags, durations, cache provenance and observation time. IDs are still internal
topology and should not be exposed publicly. The checker does not log provider
errors; applications needing deeper operational logs must apply their own
authorization and redaction policy outside this report.

## Limits

| Setting | Default / maximum |
|---|---|
| Dependencies | 64 maximum, including unselected roles |
| Roles per dependency | 16 maximum |
| Role/dependency labels | 64 ASCII characters; lowercase letter first, then lowercase letters/digits/underscore/hyphen |
| Concurrent actual callbacks | 4 / 16; timed-out callbacks retain capacity |
| Whole readiness flight | 2 seconds; configurable 1 ms–30 seconds |
| Individual dependency | 500 ms capped by flight limit; configurable 1 ms–flight limit |
| Readiness cache | 250 ms; configurable 1 ms–5 seconds; `DisableCache` explicitly disables reuse |
| HTTP observation | 5 seconds; configurable 1 ms–1 minute |

Dependency timeout starts after capacity admission; queued work is bounded by
the whole-flight deadline. HTTP policy callbacks must cooperate with their
context: Go cannot forcibly terminate arbitrary trusted callback code.

No deployment, orchestrator, background monitor, automatic role resource
discovery or process restart is installed by this package. Those integrations
remain explicit parts of the approved AgentFlow operations plan.
