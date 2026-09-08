# Bounded producer retries

`ClientConfig.PublishRetry` is optional. Nil keeps one publication attempt. A
non-nil policy lets `Client.Enqueue`, `Task.Delay` and `AcceptanceError.Retry`
recover synchronously from explicitly retryable acceptance failures. This is
separate from handler `RetryPolicy`: task retry counters never change.

| Setting | Zero-value default | Accepted range |
| --- | --- | --- |
| `MaxAttempts` | 3 total rounds, including the first | 1–10 |
| `InitialDelay` | 100 ms | 1 ms–5 s |
| `MaxDelay` | 1 s | InitialDelay–5 s |
| `MaxElapsed` | 5 s | 1 ms–30 s |
| `DisableJitter` | false | Exponential full jitter is enabled |
| `Retryable` | Exact `ErrBusy` or `ErrUnavailable` only | Optional trusted read-only classifier |

Each round makes at most one broker/scheduler call and one result-registration
call. Delays double to the cap; jitter selects between zero and that delay.
Context cancellation or the elapsed deadline interrupts waiting. The deadline
covers preparation, authorization and providers too, but all callbacks/providers
must cooperate: there is no detached goroutine or forced interruption. Clock and
classifier callbacks and error-chain methods must return promptly. Policy values
are copied at construction; configure provider implementations before use.

The optional classifier can recognize an application's transport errors. It
cannot override invalid input, identity conflict, denial, unknown task, frozen
configuration, recorded cancellation, or context cancellation/deadline errors,
including errors joined with one of those failures. Classifier/provider panics
stop the operation with a safe error; they are not retried automatically.

One private operation snapshot retains the client ports, policy, task ID, JSON
payload, creation time, route and converted countdown. Each provider receives a
fresh detached envelope. The first selected broker/scheduler does not change
when a retry crosses ETA. Enqueue authority and context are checked before each
write; expiry prevents another transport attempt. Once transport acceptance is
confirmed, only result registration is retried, even after task expiry: that
repairs metadata for an already accepted task rather than creating new work.

An acknowledgement can be lost after a write applied. Repeating the same task ID
and digest permits adapter deduplication, but the Broker contract is at-least-once
and arbitrary adapters or lost Redis state can deliver duplicates. No replay
capability declaration or exactly-once guarantee is inferred. Keep handlers and
external effects idempotent. Internal outbox and workflow-intent relays remain
one-shot per durable attempt; this option adds no sleeps to their leased work.

Always retain an `AcceptanceError` from a possibly applied operation. Its safe
`ID` identifies the original logical task; `Confirmed` says transport acceptance
was observed, not execution or completed result registration. A later denial,
cancellation, panic or exhausted retry budget still preserves these facts and
the original envelope. Calling its `Retry` method uses private recovery state,
not edited exported descriptive fields. A confirmed failure resumes registration
only; an unconfirmed failure may repeat transport. Retry never invents a new ID.
Supply the same configured backend authority when explicitly retrying; the API
does not migrate acceptance between unrelated providers. `Task.Delay` captures
its task/client handles and option sequence before argument/option callbacks;
its returned result remains bound to that client even if the original pointer is
subsequently replaced. Typed preparation also consumes the configured elapsed
budget, although a noncooperative serializer cannot be forcibly interrupted.
Errors before any acceptance attempt remain ordinary validation/access errors.
On success, the configured acceptance observer runs once; observation remains
best-effort and cannot undo acceptance. Do not log provider causes without an
application-owned redaction policy.
