# Deployment and process roles

Build one application and run the processes it explicitly configures. Gogo is a library/framework repository; deployment credentials belong to your client project.

## Same binary, separate processes

| Process | Command after project wiring | Responsibility |
| --- | --- | --- |
| Web | `./manage serve` | Configured HTTP handler |
| Worker | `./manage worker` | Configured task queues and recovery components |
| Scheduler | `./manage beat` | Configured periodic schedules and relay |
| Migration job | `./manage migrate` | Explicit database schema changes |

The worker and Beat commands exist only when their factories are registered. Keep migration execution separate from serving or consuming work. The showcase's web/worker containers demonstrate command separation, not a complete production deployment recipe.

## Runtime modes are not built in yet

There is currently no built-in `GOGO_MODE`, `GOGO_MODES` or custom-mode registry. `GOGO_ENV` selects development/test/production behavior; it does not select a worker or router.

Applications can implement their own declared setting and select a handler/command explicitly. A first-class mode registry with per-mode routes, resource selection and lifecycle remains proposed work, not an API documented as shipped.

## Production checklist

- Pin compatible module versions and record the built revision/toolchain.
- Validate required configuration before starting the selected process; keep secrets outside the image/binary.
- Bind the HTTP listener appropriately inside its runtime, then restrict exposure through deployment networking.
- Configure HTTPS, allowed hosts and only the actual trusted proxy CIDRs.
- Use verified PostgreSQL/Redis transport and durable Redis policies for durable roles.
- Apply reviewed migrations through a controlled separate operation with backup/restore planning.
- Configure process-specific readiness, bounded dependency checks and graceful shutdown.
- Bound worker concurrency and match application deadlines to actual cooperative/subprocess behavior.
- Keep shared cross-process state in the selected database/queue, not a process-local map.
- Test denied paths, uncertain writes, restarts and restore behavior in your actual topology.

## Shutdown and scaling

Stop admission, drain in-flight HTTP/WebSocket/task work, then close resources. Cancellation requests cooperation; it cannot forcibly stop arbitrary Go callbacks. A healthy HTTP probe does not prove every background goroutine is progressing.

Scale consumers only when task handlers and shared coordination tolerate repeated delivery. Periodic schedule fencing is not namespace-wide leader election. Protect external side effects with business idempotency rather than relying on pod count.

## What is not certified

This alpha does not include a universal Kubernetes deployment, production restore drill, complete failover proof or full load/accessibility audit. Use [health and telemetry](observability.md), the connector contracts and your own operational tests before deciding it is suitable for a production workload.
