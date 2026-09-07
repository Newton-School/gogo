# Single-task result boundaries

`Result.Snapshot`, `Ready`, `Successful`, `Failed`, `Get` and `Wait` observe one
task. `Forget` and `Revoke` retain their separate mutation grants after the
existing scoped read grant. The low-level `ResultStore` remains a trusted
provider port, not an authorized application API.

| Boundary | Behavior |
| --- | --- |
| Handle | Each call captures its client and task ID before callbacks. A whole `Get`/`Wait` loop uses that identity. Editing `Receipt` or replacing the handle inside a callback affects only later calls. Concurrent unsynchronized handle edits are not supported. |
| Lookup | A valid UUID task ID must match the returned envelope exactly. Envelope digest, revision, state, delivery count and bounded metadata are validated before authorization. Unknown task and expired payload remain distinct. |
| Read grant | The existing `read` operation uses the returned task scope and frozen task ID. Record metadata is deeply detached before this callback, including JSON bytes, headers, signatures and failure data. |
| Read failure | Malformed responses, callback/provider panic and unavailable providers return no record. Provider error text is never returned. Exact standard not-found, expired, denied and context sentinels retain their meaning; ambiguous wrapped/joined provider errors become `ErrUnavailable`. |
| Wait | Every poll authorizes the same frozen task. Context cancellation ends waiting, not execution. Worker-side synchronous waits remain forbidden. Invalid typed output decoding or a decoder panic returns a zero output and a safe error. |
| Forget | Authorize `read`, then `forget`, then invoke the provider with the same task ID. Providers retain workflow pins and replay tombstones. |
| Revoke | Authorize `read`, then `revoke`, then send the same task ID and observed scope. This requests cooperative cancellation; it does not prove the handler has stopped or undo external effects. |
| Mutation reply | Cancellation before provider invocation prevents the command. A confirmed reply is preserved if cancellation happens afterward. Errors or panics after invocation can follow an applied command and are not proof of unchanged state. Reconcile through a fresh authorized read. |

Read metadata is bounded before JSON detachment: the envelope retains its 256 KiB
wire budget and existing callback-list/depth limits (32 per list, depth 16);
output is at most 256 KiB, progress 16 KiB, and public failure code/message are
at most 256/4,096 bytes. The complete serialized record is at most 768 KiB.
Length checks precede UTF-8 scans on variable-size strings. Invalid UTF-8 or JSON
is rejected instead of silently replacing or discarding data. Retained payload
absence is valid and never fabricated as a successful decoded value. Public
`Failure` content remains the producer's safe-diagnostic contract, not raw
provider error text.

JSON detachment preserves wire instants and numeric payload bytes; it does not
retain process-local monotonic clock readings or named-zone metadata absent from
the wire format. Each returned timestamp also gets an independent location value,
including timestamps inside nested callback signatures. Assigning through its
`Location()` pointer cannot mutate provider-owned or other returned timestamps.

This boundary does not change durable state transitions, result retention,
Redis record layout, worker fencing, or `GroupResult` behavior. Raw provider
implementations must still honor cancellation and maintain atomic state rules.
