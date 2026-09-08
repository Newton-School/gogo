# Process-local signals

`Signal[T]` synchronously dispatches the supplied value and context. Receiver IDs
are unique within a signal. Lower priorities run first; equal priorities retain
registration order. A send snapshots registrations before calling receivers or
context methods. Connecting or disconnecting affects later sends, including when
another send is already running. Returned result slices belong to the caller.

`Send` stops after the first receiver error. `SendRobust` collects receiver errors
and continues. Both convert receiver panics to a safe error, check context at entry
and after each receiver, and stop when a context failure is observed. These checks
are cooperative; an in-flight receiver is not interrupted or moved to a goroutine.
An already canceled context fails even with no receivers. Nil or typed-nil
contexts, or contexts whose `Err` method panics, return `invalid signal context`
without revealing panic values.

Results contain only invoked receivers, in invocation order, with their own exact
errors. A final receiver that succeeds and cancels its context retains a successful
result, while the dispatch returns cancellation. Collected receiver errors and an
observed context failure are joined. Dispatch errors do not undo receiver effects:
the sender owns transaction and after-commit semantics.

Payloads are not deep-copied; reference-bearing values retain their ordinary Go
semantics. Signals provide neither durable replay nor cross-process delivery.
This package's dispatcher does not itself install request, ORM, migration, test,
or task lifecycle integrations.
