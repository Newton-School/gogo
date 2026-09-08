# Minimal task dispatch

`core/tasks` defines a small, provider-neutral `Dispatcher` and an identity-bound
`Result`. Core does not import Async or Redis. No provider is installed by default;
selecting a nil provider is an application configuration error, not permission to
run tasks inline. There is no worker, scheduler, registry discovery or automatic
retry in this package. Test doubles are for explicitly selected testing use only.

Application code can accept a `tasks.Dispatcher`, enqueue a versioned task with
JSON arguments, and call the returned handle's `ID` and `Get`. Production
composition can opt into the separately installed Async module:

```go
dispatcher, err := async.NewCoreDispatcher(configuredClient)
if err != nil {
    return err
}
result, err := dispatcher.Enqueue(ctx, tasks.Request{
    Task: "reports.count", Version: 1,
    Args: json.RawMessage(`{"report":"summary"}`),
    Scope: "reporting",
})
if err != nil {
    var acceptance *tasks.AcceptanceError
    if errors.As(err, &acceptance) {
        // Retain acceptance.ID and acceptance.Confirmed for reconciliation.
        // result is nonnil, but its existence does not prove task execution.
        // Do not reconstruct the request or assign a new ID as an automatic retry.
    }
    return err
}
output, err := result.Get(ctx)
```

This snippet uses the standard `context`, `encoding/json` and `errors` contracts;
`configuredClient` must come from `async.NewClient` with real selected providers,
task descriptors and the application's authorization policy. Async registration
and worker startup remain separate, explicit operations. The bridge owns none of
their lifetimes. Producer-only descriptors may omit executable task handlers;
workers must have the matching registered name and version.

## Admission and results

A nil enqueue error confirms admission, **not execution**. Definite validation,
configuration or authorization refusal returns no result. After a genuine
publication attempt, `AcceptanceError` preserves its stable ID and whether
transport acceptance was observed, together with a nonnil result handle. A
confirmed transport write can still fail result registration. An unknown result
after uncertain admission does not prove that no work was accepted.

The bridge adds no retry layer. An explicitly configured Async `PublishRetry`
policy still applies. Start recovery classification from the portable
`*tasks.AcceptanceError` and nonnil result returned by this enqueue operation.
Only then may optional Async-aware recovery inspect its cause with `errors.As`
for the original `*async.AcceptanceError` and its exact-envelope `Retry`.
Reconstructing JSON with the same ID is not equivalent. A pre-publication callback
error can retain an unrelated Async acceptance error as a diagnostic cause; that
does not establish admission or authorize replay for this enqueue. Provider
contracts permit duplicate delivery and unknown acknowledgements, not exactly-once
execution.

`Get` returns detached JSON, using current result authorization. It preserves
JSON number precision without a float conversion and strips no result fields.
`ErrNotFound`, `ErrResultExpired`, `ErrDenied`, `ErrUnavailable` and `ErrWorkerJoin`
remain distinct portable outcomes. Terminal `tasks.Failure` carries only the
existing safe public code/message; a task's `EXPIRED` or `REVOKED` outcome is not
the same as missing retained output. Cancellation remains discoverable with
`errors.Is`. Provider causes are available for deliberate local inspection, never
included by the portable operational/acceptance error formatter.

## Boundaries

The Async bridge freezes its configured client by value and snapshots request
JSON before any context, clock, validation, authorization or provider callback.
Returned results retain that configuration even if the original public client is
replaced. This does not clone live providers or make concurrently mutated caller
objects safe: providers and inherited context values remain trusted components.

Raw JSON is valid UTF-8 and at most 256 KiB. Task/queue names use the Async ASCII
name grammar and at most 192 bytes; IDs are lowercase UUIDs, versions are positive,
and scopes are at most 256 UTF-8 bytes. Async additionally validates the registered
payload schema, route allowlist and complete 256 KiB envelope; metadata means the
maximum usable argument body can be smaller. Scope is an input to authorization,
never a grant. Do not put credentials or secrets in ordinary task payloads.

Callbacks and provider calls are cooperative: use bounded contexts; the port does
not forcibly terminate a blocked callback. Worker-context synchronous waits are
refused. No `Snapshot`, restore-by-ID, cancellation, forgetting, ETA/countdown,
headers or canvas APIs are introduced here; full Async owns those capabilities.
Third-party implementations of the port must uphold the documented validation,
ownership, authorization, admission, retention and safe-error contracts themselves.
