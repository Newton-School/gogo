# Explicit operation telemetry

This is the first, manually instrumented slice of the approved telemetry feature.
It provides context correlation, sampled operation spans, a bounded export queue,
local delivery counters and a `slog`/JSON console exporter. It does **not** yet
provide automatic HTTP/ORM/Async instrumentation, metrics aggregation/exporters,
OTel adapters, error-reporting integrations or profiling endpoints.

```go
exporter, err := observability.NewConsoleExporter(os.Stderr, slog.LevelInfo)
if err != nil {
    return err
}
telemetry, err := observability.New(observability.Config{
    Operations: []string{"http.request", "db.select"},
    Routes: []string{"/books/<int:id>/"},
    SampleRate: observability.SampleAll,
    QueueSize: 256,
}, exporter)
if err != nil {
    return err
}

next, span, err := telemetry.Start(ctx, "http.request", "/books/<int:id>/")
if err == nil {
    ctx = next
    // Pass ctx explicitly into application work and any child spans.
    // Record a coarse outcome; never pass errors, SQL or request payloads.
    defer span.End(observability.StatusSuccess)
}
```

Choose the actual final status in application code; the example's deferred
success is appropriate only when that outcome is known. Telemetry failures are
not transaction failures: an unsuccessful `Start` should not overwrite the
application's original context or cause a business-operation retry.

## Data and admission

1. `New` snapshots developer-declared operation/route allowlists. Runtime names
   must match exactly. Declare templates such as `/books/<int:id>/`, not resolved
   paths, IDs, SQL, tokens or other data. Empty route means a non-HTTP operation.
2. `Start` copies only explicit `Correlation` IDs from the caller's context. It
   creates a trace when absent and a fresh child span. Request/task IDs are
   optional, trusted opaque values supplied using `WithCorrelation`.
3. Sampling uses the trace ID and the configured parts-per-million rate. Zero
   disables emission; `SampleAll` samples every span. A trace's choice is stable
   within one pipeline. Incoming sampled flags and HTTP headers are not parsed.
4. The first concurrent `Span.End` records a finite status and native monotonic
   duration, clamped to `[0, 24h]`. It never invokes an exporter or waits for queue
   capacity. `true` means admitted, **not** delivered. Later `End` calls do nothing.
5. One worker exports read-only value events. No caller contexts, user attributes,
   error objects, query values, task bodies or arbitrary closures enter records.
   Exporter contexts are clean, package-owned contexts with bounded deadlines.

Operation names are at most 96 ASCII bytes; route templates at most 256 ASCII
bytes. Configuration accepts at most 256 operations and 4096 routes, rejects
duplicates/control characters/query syntax and requires routes to start with `/`.
The queue defaults to 256 and is limited to 4096 entries, plus one active record
and one shutdown request. Flush barriers share those same queue slots. Opaque
IDs have fixed 16-byte trace/request/task and 8-byte span representations; `String`
renders lowercase hexadecimal. Correlation IDs are log/span correlation fields,
**never metric labels**. The fixed grammar is a bound, not a sanitizer that can
make credentials safe: configuration and externally supplied IDs are trusted.

`Stats` is a local, non-transactional snapshot of atomic counters. `Dropped`
includes full/closed queue admission, invalid end status, failed exports and
records abandoned during canceled shutdown. `SampledOut` is separate.
`ExportFailures` counts exporter error/panic results without retaining the error
or panic value. `Exported` counts completed exporter calls returning nil; it does
not establish durability or remote receipt. Disabled slog levels also complete
without a reported exporter error.

## Lifecycle and sink responsibility

- Keep one explicit pipeline owner. Close it after requests/tasks have drained,
  for example through the closer returned by an application `Resource.Open`.
  Construction does not register global hooks or read environment variables.
- `Flush(ctx)` enqueues a barrier without waiting for capacity (`ErrBusy` when
  full). Success means prior admitted records finished their export attempts and
  the exporter flush succeeded, not that dropped/failed records were delivered;
  `Stats` preserves those outcomes. Concurrent later events are not covered.
  Caller cancellation/deadline bounds waiting. Canceled barriers occupy only
  their existing bounded slot until the worker consumes them.
- `Close(ctx)` stops admission once, drains accepted work, then attempts exporter
  flush and close once. The first caller owns cleanup's context; concurrent and
  subsequent callers only wait for that same shutdown. Export calls default to a
  1-second timeout; lifecycle calls to 30 seconds when the caller has no earlier
  deadline. Both configured limits must be positive and at most one minute.
- A shutdown timeout cancels in-progress cooperative export/flush, drops queued
  records and skips queued flush barriers. Final exporter flush/close are still
  attempted with the expired context. Already admitted work is never retried.
- All exporter methods are serial. A non-cooperative exporter can retain **one**
  worker/callback after `Flush`/`Close` returns; Go cannot kill it. Cleanup is not
  proven complete until that callback returns and the worker finishes. There is
  no goroutine-per-export timeout escape and no unbounded retry queue.
- Exporters must bound their own internal buffers and honor context cancellation.
  They must not call `Flush`/`Close` on their own pipeline. A custom slog handler
  is trusted code and may add its own fields; Gogo emits only its fixed keys.
  The console exporter uses a fresh JSON handler without inherited logger fields.
  Its flush/close do not flush or close the caller-owned writer. A blocking writer
  cannot be interrupted by this package merely because its context expired.

The existing application settings `GOGO_LOG_LEVEL` and `GOGO_SHUTDOWN_GRACE` can be
mapped into this explicit configuration by the application. This slice does not
add environment settings or change their validation/defaults.
