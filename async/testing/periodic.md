# Periodic schedules with a fake clock

`testing.NewMemory()` explicitly implements the existing `async.PeriodicStore`
alongside its test broker, result, delayed-work and intent ports. Production
configuration never selects it automatically. It is process-local, not durable,
and does not implement database-wide Beat leadership.

Set `memory.Clock` to a test-owned clock, and give the same clock to the Client
and Worker. Install a `PeriodicSchedule` with revision 1 and expected revision 0;
then drive `Beat.Tick`, `IntentRelay.Tick`, `Worker.Process` and
`RestoreResult(...).Get` separately. Advancing your clock replaces sleeps; no
goroutine or scheduler is started for you. The executable
`ExampleMemory_UpsertSchedule` demonstrates the complete path.

## Lifecycle and outcomes

- `UpsertSchedule` creates or replaces a row only at the expected revision;
  callers supply the next revision. An update clears the prior lease and its
  observed timestamp. Enable/disable is explicit revisioned state.
- `LeaseSchedules` selects enabled due rows, increments both revision and fence,
  and records owner, lease deadline and observed time. Another owner must wait
  for expiry or an explicit revisioned update. All selected rows are checked
  before any row is leased; exhausted counters refuse the batch.
- `CommitOccurrence` requires the same revision, fence and owner with an
  unexpired lease. It checks the entire intent batch before atomically advancing
  `NextDue`, recording `LastTaskID`, releasing the lease and adding intents. A
  next occurrence at or beyond `EndAt` disables future scheduling.
- Disable/update affects future scheduling, not an already-retained intent,
  accepted task, running worker or result. Occurrence IDs and task retry counts
  are still owned by the existing Beat/worker protocols.

Use `Fail` operation names `upsert_schedule`, `lease_schedules`,
`commit_occurrence`, `disable_schedule`, and `list_schedules` for **pre-write**
failure injection. Other Memory operation names and behavior are unchanged.
For an applied-commit/lost-reply case, explicitly wrap `PeriodicStore`, delegate
`CommitOccurrence`, then return an injected error. An error by itself is never
proof of no effect. Inspect the retained schedule/intents; do not blindly rerun
or create a new schedule/task ID. A stale commit retry is refused even when its
original write succeeded. No automatic retries are added.

Inputs are bounded and detached before context, Clock or Fail callbacks.
Callbacks are trusted, cooperative and run outside the periodic state mutex;
their errors are retained for deliberate test inspection, while panic and
nonconforming context errors become `async.ErrUnavailable`. Cancellation is
checked before mutation; no post-commit context callback changes a confirmed
return. The clock is sampled once per lease/commit, outside the lock. Configure
Clock/Fail before use; concurrent assignment to their public fields, replacing
the whole Memory value, and concurrent mutation of input data are unsupported.
The existing non-periodic Memory operations are not refactored by this feature.

## Explicit simulator boundaries

This is not a blanket Redis parity claim:

| Boundary | Memory periodic operations | Existing Redis adapter |
| --- | --- | --- |
| Time authority | Explicit fake clock, positive Unix milliseconds only | Redis server time |
| Due/lease precision | Due comparisons and observed/lease values use milliseconds; stored `NextDue` and rule intervals retain nanoseconds | Same due-index and metadata precision; opaque payload retains nanoseconds |
| Ordering/atomic scope | Due then ID lease order; ID list order; one process mutex for the selected batch | Partitioned Redis leases, weak bounded inventory; no global ordering or multi-partition atomicity claim |
| Owner input | 1–256 UTF-8 bytes without NUL/CR/LF | Current adapter requires nonempty owner only |
| Missing Disable | `async.ErrNotFound` | Current Redis missing-row error |
| Conflicting retained intent ID | `async.ErrConflict`, no schedule advance or overwrite | Current adapter preserves the first intent without comparing its content and may advance the schedule |

Additional simulator safety bounds: list/lease limits 1–1000; schedule JSON at
most 256 KiB; occurrence batches at most 1000 intents and 8 MiB JSON; recursive
framework data depth at most 32. The preflight visit budgets are 16,384 for a
schedule and 1,048,576 for a batch, including structural fields. Present intent
envelopes must pass the existing protocol validation. These bounded refusals
are not new Redis wire limits. Task registry/argument validators still run in
Beat when dispatching; storing metadata does not authorize or prove executable
work. Keep native Redis tests for server time, durable storage, lease takeover,
transport outcomes, and deployed adapter behavior.
