---
name: create-update-agentflow-architecture
description: Create or revise accurate AgentFlow table, flow, and document pages from requirements, code, and review feedback before implementation.
---

# Create Or Update AgentFlow Architecture

Use this skill when work changes product behavior, data shape, APIs, permissions, jobs, queues,
events, integrations, or cross-module contracts.

## Read Before Drawing

1. Read the user request and authoritative requirements.
2. Inspect the existing implementation and repository conventions when code exists.
3. Read relevant rules and memories, especially architecture feedback.
4. Read both architecture states:

 ```bash
python3 .agentflow/scripts/agentflow.py snapshot --state implemented
python3 .agentflow/scripts/agentflow.py snapshot --state current
```

Use `--page <page-id>` when only one page is relevant. Do not assume current architecture is
implemented when `status` reports different architecture hashes.

## Choose The Hierarchy And Page Type

The client controls the complete navigation tree. Infer its preferred names, nesting, and order
from the existing architecture and user request. Reuse existing folders when they still fit. Do not
create mandatory Apps, Features, Models, Flows, or Documentation sections.

Use one of three page types:

- `table` for models, fields, enums, permissions, configuration matrices, and other structured data;
- `flow` for user paths, API orchestration, jobs, event handling, integrations, and system behavior;
- `document` only for short information that is materially clearer as prose or a compact reference.

Prefer a table or flow whenever possible. A developer should not need to read long documents to
understand the architecture.

## Draw Simple, Complete Flows

Use only the fixed node types below. The renderer owns their shape and color.

Build every flow from top to bottom. Omit `layout` or set `layout.direction` to `down`; use
side-by-side branches only for paths that happen at the same stage of the flow.

| Type | Use for |
| --- | --- |
| `actor` | Person or initiating role |
| `interface` | Screen, dialog, form, or client boundary |
| `action` | User or system action without a stronger technical type |
| `api` | HTTP, RPC, CLI, or public handler boundary |
| `decision` | Branch with at least two labeled outcomes |
| `database` | Persistent read or write |
| `cache` | Redis or another cache operation |
| `queue` | Message broker or queued handoff |
| `job` | Asynchronous Celery, worker, cron, or background execution |
| `event` | Published or consumed domain event |
| `external` | Third-party or separately owned service |
| `subprocess` | A referenced flow that is documented elsewhere |
| `terminal` | Successful or neutral outcome |
| `error` | Rejected or failed outcome |

Keep titles short and conversational. Put exact routes, function names, tables, tasks, queues,
events, retry rules, transaction boundaries, and other implementation details in `subtitle` and
`details`. Never represent an entire function as one node when it contains multiple
architecture-significant operations. Split those operations into concrete actions, decisions,
database/cache operations, queue handoffs, async jobs, events, and outcomes. Omit ordinary code
mechanics that do not affect product behavior. Use groups only when they clarify ownership such as
request transaction, worker, or external provider.

Every node must be reachable. Every non-terminal node except a fire-and-forget `job` must lead
somewhere. A `job` may be a leaf only when the caller intentionally does not wait for worker
execution; do not add a fake terminal or edge to satisfy the diagram. A decision needs at least two
labeled outgoing edges. `terminal` and `error` nodes cannot have outgoing edges. Use `async` or
`error` edge styles only when they carry real meaning.

## Author The Proposal

Read `.agentflow/references/proposal-format.md`. A proposal contains only explicit operations:

- `set-navigation` replaces the ordered navigation tree;
- `upsert-page` adds or replaces one complete page;
- `delete-page` removes one page.

Omission never deletes content. IDs must remain stable. When a page changes, submit its complete
new value so validation can inspect the resulting architecture.

Create or revise, validate, submit, and regenerate:

```bash
python3 .agentflow/scripts/agentflow.py proposal create --input /tmp/proposal.json
python3 .agentflow/scripts/agentflow.py validate --proposal <proposal-id>
python3 .agentflow/scripts/agentflow.py proposal submit <proposal-id>
make agentflow PROPOSAL=<proposal-id>
```

Use `proposal revise <proposal-id> --input ...` for an active proposal. Do not open the generated
page automatically. Tell the user to refresh their existing tab or open `.agentflow/index.html`.

## Reconcile Before Review

Before submitting, compare the candidate architecture with the request and code:

- every requested behavior and data change is visible;
- important branches, failures, async boundaries, and terminal outcomes are present;
- structured definitions are complete enough to plan without invention;
- no behavior outside the request has been introduced;
- navigation order and names match the client's chosen hierarchy;
- document pages are concise and necessary.

Stop before planning or implementation. Approval must be explicit.
