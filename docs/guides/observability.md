# Health checks and telemetry

Health answers whether a process can accept work. Telemetry records explicitly instrumented operations. Neither should expose secrets or be confused with proof of every business operation's success.

## Health probe types

| Probe | Question | Dependency I/O |
| --- | --- | --- |
| Liveness | Is the supplied process state live? | No |
| Startup | Did required startup complete? | No |
| Readiness | Is this role started, accepting work and sufficiently healthy? | Only configured dependencies |

Create `health.New` with a role, state callback and bounded dependency probes. `health.FromApplication` adapts the application lifecycle. Mount handlers created by `health.NewHandler` in your URL tree; constructing a checker does not start a server or background polling.

## Role-specific dependencies

A worker may need its broker while a public API may need only PostgreSQL. Configure dependency roles accordingly. An empty role list applies the dependency to every role, not none.

Outages affect readiness independently of process liveness. Draining immediately stops readiness. Optional/degraded behavior must be explicitly allowed; marking a critical dependency optional is not an outage strategy.

Public health responses expose bounded status, not provider details, addresses or credentials. Put detailed diagnostics behind authorization. A blocked or noncooperative probe retains its work budget; repeated requests must not create unbounded duplicate callbacks.

## Telemetry pipeline

`core/observability` provides explicit bounded operation/span events, local delivery counters and a `slog`/JSON console exporter. Construct its pipeline, instrument selected operations, and flush/close it as part of owned shutdown.

Use stable operation names and safe identifiers. Do not attach passwords, tokens, raw SQL parameters, task payloads or unrestricted request bodies. Telemetry failure must not rewrite an already confirmed business result.

There is no automatic whole-stack tracing merely from importing the package. Network exporters and complete instrumentation coverage are not implied. The [health](https://github.com/Newton-School/gogo/tree/master/examples/showcase/recipes/health) and [telemetry](https://github.com/Newton-School/gogo/tree/master/examples/showcase/recipes/telemetry) recipes show explicit wiring.
