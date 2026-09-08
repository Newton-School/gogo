# Forgetting flat-group payloads

`GroupResult.Forget(ctx)` releases a terminal flat `Group`'s child output and
progress plus the output copies retained by its workflow graph. It does not
cancel tasks, wait for completion, drain a relay, or delete the workflow.
Chains, chords and compiled nested canvases are deliberately unsupported.

The workflow backend must implement the optional `WorkflowPayloadStore` port;
the Redis adapter and explicit testing-memory backend do. An unsupported backend
is refused before child deletion. Existing `WorkflowStore` implementations do
not acquire a new mandatory method.

The caller needs current `read` and `forget` grants for the group and every
affected child under its exact scope. All identities, grants and pin states are
preflighted before the first write, and relevant grants are repeated before
subsequent writes. A live group or workflow-pinned child returns `ErrPinned`;
finish the ordinary worker/relay workflow before retrying. Forget itself does
not consume worker capacity waiting for that progress.

Child payloads are released first. A final scope/revision-checked graph CAS
clears aggregate/member outputs and sets `Graph.PayloadForgotten`. It retains
terminal states, failure metadata, member identities, dispatch metadata, intent
records, pin metadata and replay tombstones. This is payload release through the
result APIs, **not secure erasure** of task arguments or historical intent copies,
not automatic retention scheduling, and not workflow/inventory garbage collection.

After success, `Snapshot` and `Ready`/`Successful`/`Failed` retain their metadata
semantics. `Join`, `JoinOutcomes` and `Iterate` return `ErrResultExpired`, including
for an empty group or failed group; retained failure details remain available
through `Snapshot`. Readers do not reconstruct a forgotten group from child
lookups. An already-running iterator can retain genuinely yielded outcomes before
a later poll observes Forget; Forget cannot revoke bytes already read by callers.
False `PayloadForgotten` is omitted from JSON, leaving existing graph wire forms
unchanged. A true marker is valid only on a terminal flat group with no output
copies, and must be understood by readers using this retention feature.

The result and workflow stores are independent. An error may follow some child
deletions, or a fully applied final CAS whose acknowledgement was lost. There is
no rollback or all-or-nothing promise. Retrying the same identity is safe; an
exact missing child can mean its retention already elapsed, but an outage is not
treated as absence. Nil means the required replies were confirmed, not that this
invocation necessarily performed every deletion. Cancellation after the final
confirmed reply does not change success. Provider errors/panics expose safe
errors and do not prove that payloads remain retained.

Configure ports and grants before sharing clients. Each operation freezes its
handle and client configuration; provider internals and authorization callbacks
remain trusted, cooperative infrastructure. The existing result port owns atomic
pin enforcement and stable task identities. The graph transition is bounded by
the existing 1,000-child/8 MiB workflow limits; child records are individually
bounded and are not collected into an additional payload batch.

```go
group := async.RestoreGroup(client, knownGroupID)
if err := group.Forget(ctx); err != nil {
    // ErrPinned: let ordinary workflow progress finish.
    // Other failures: some deletions may already have happened. Reauthorize
    // and retry the same group; do not assume rollback or re-enqueue tasks.
    return err
}
```

`ForgetGroupPayload` is a pure adapter transition, not an authorized application
operation. `WorkflowPayloadStore.ForgetGraphPayload` must atomically enforce the
exact ID, scope and expected revision, retain existing coordination records, and
never recreate missing state. Applications should use the GroupResult method.
