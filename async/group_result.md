# Workflow result boundaries

`GroupResult` handles groups, chains, chords and compiled nested canvases.
`Snapshot`, `Ready`, `Successful`, `Failed`, `Join` and `Revoke` keep the existing
scoped authorization chain. Only a flat group supports `Iterate` and
`JoinOutcomes`; a chain's skipped nodes and a chord's body are not flat members.

| Boundary | Behavior |
| --- | --- |
| Identity | Each operation captures its client configuration/ports and UUID workflow ID before callbacks. A complete wait or running iterator uses that identity even if a callback or consumer replaces the public handle or client value. Provider object internals are still trusted provider behavior, not copied state. Unsynchronized concurrent handle/client edits are unsupported. |
| Provider read | Require the exact graph ID, positive revision, valid workflow kind/state, matching child scopes, valid topology and terminal-member identities. Malformed or over-budget observations disclose no graph or new member, including when another member appears valid. |
| Bounded metadata | Retain the existing 8 MiB durable graph limit, 1,000 tasks, 2,000 logical nodes and 256 KiB child envelope limit. Signature-only binding metadata and overwritten original ID options use the graph budget, not an invented per-signature envelope cap. Preflight variable-size metadata before whole-graph encoding; reject invalid UTF-8/JSON, oversized failures and cyclic/deep callback trees. |
| Detachment | Copy graph maps, JSON payloads, signatures, failures and dependency slices before the read grant. Copy every timestamp location separately, including nested signature options. JSON preserves wire instants, not process-local monotonic data or zone names absent from the wire. |
| Read errors | Provider/callback panic, malformed data or private provider errors become safe errors with no partial snapshot. Exact not-found, expired, denied and context sentinels retain meaning; ambiguous wrapped/joined provider errors become `ErrUnavailable`. |
| Polling | Every poll authorizes the frozen workflow. Scope, ordered membership and compiled topology cannot change across a wait. Context cancellation stops waiting, not execution; worker-side synchronous waits remain forbidden. |
| Wait contexts | `Join`, `JoinOutcomes` and `Iterate` already wait; no separate `Get`/`Wait` alias is needed. Nil/typed-nil contexts are invalid; panicking or nonconforming context methods fail safely when invoked. A nonterminal wait whose `Done` channel closes with nil `Err` returns unavailable, never successful completion or a synthetic member. Empty and nonempty terminal completion check cancellation. |
| Join | Propagate terminal failure/revocation. A successful workflow with an absent retained output returns `ErrResultExpired`, never an invented value. Flat-group and logical-collector output must be a JSON array with the expected member count; scalar chain/chord/task-root output remains one returned raw value. |
| Iterate | Yield each real terminal flat-group member once, with its original index. Reauthorize before each yield. A missing copied successful payload is recovered through an independently authorized task-result lookup, then the workflow is reauthorized again before yielding. Cancellation or denial exposes no subsequent member. |
| JoinOutcomes | Return observed terminal members in original relative order, including member failures. A later read/permission/context error accompanies only genuine previously observed members. Collection is capped at 8 MiB; streaming can recover more aggregate child payload data while each durable graph remains bounded. |
| Revoke | Authorize `read`, then `revoke`, then call the captured provider with the same workflow ID and observed scope. Cancellation before invocation prevents the call. Confirmed replies survive cancellation afterward; errors/panics after invocation may follow an applied command and do not prove unchanged state. Reconcile with a fresh authorized read. |

Application code must treat public `Failure` content as the producer's safe
diagnostic contract. Raw provider error messages are never returned. This
boundary does not change graph persistence, workflow CAS, pinning, retention,
task execution or cancellation propagation. Low-level `WorkflowStore` remains a
trusted provider port, not an authorization-bearing application API.

An iterator consumer that accepts the last member and then cancels the context
receives a following cancellation error, retaining that genuine member. A consumer
that breaks the range stops immediately, without another context/provider/grant
call. Consumer panics propagate unchanged; context error handling does not recover
across `yield`. Context and provider callbacks remain cooperative, not forcibly
terminated. These guards add no timeout, task retry or cancellation request.
