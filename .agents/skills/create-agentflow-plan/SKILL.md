---
name: create-agentflow-plan
description: Create and register an implementation plan that covers an approved AgentFlow architecture change exactly, without missing or adding scope.
---

# Create AgentFlow Plan

Use this skill only after the relevant AgentFlow proposal is explicitly approved.

## Contract

The approved architecture is the complete scope boundary. The plan must include every changed
navigation item and every changed table, flow, or document element. It must not introduce behavior
or data that is absent from the approved architecture.

If planning exposes a missing decision, do not invent it in the plan. Create a new architecture
proposal and wait for approval.

## Read The Approved Change

Read repository instructions, relevant memories, and current state:

```bash
python3 .agentflow/scripts/agentflow.py status
python3 .agentflow/scripts/agentflow.py snapshot --state current
python3 .agentflow/scripts/agentflow.py proposal list
```

Use the `architectureHash` and approved proposal that produced the current architecture. Inspect
the existing code so plan steps follow repository conventions, but do not let existing code add
product scope.

## Write The Human Plan

Create `.agentflow/plans/<plan-id>.md`. Organize it in implementation order. Each task should state:

- the exact architecture elements it implements;
- files, modules, migrations, or symbols expected to change when known;
- dependencies and transaction or asynchronous ordering;
- verification that proves the represented outcome;
- explicit non-goals where nearby behavior could be accidentally included.

Cover the full paths, not only happy-path code: models and constraints, decisions, permissions,
failures, jobs, queues, events, integrations, and terminal outcomes.

## Create The Traceability Manifest

Create a temporary manifest and assign every required source reference to exactly one plan task:

```json
{
  "schemaVersion": 3,
  "id": "add-invite-delivery",
  "title": "Implement audited invitation delivery",
  "architectureHash": "<current architecture hash>",
  "proposalId": "add-invite-delivery",
  "document": ".agentflow/plans/add-invite-delivery.md",
  "tasks": [
    {
      "id": "persist-delivery-attempts",
      "title": "Persist invitation delivery attempts",
      "description": "Add the approved table and constraints.",
      "sourceRefs": [
        "page:tenancy.invite-delivery",
        "page:tenancy.invite-delivery#row:status"
      ]
    }
  ]
}
```

References use:

- `navigation:<navigation-id>`
- `page:<page-id>`
- `page:<page-id>#column:<id>` or `#row:<id>` or `#cell:<row-id>.<column-id>`
- `page:<page-id>#node:<id>` or `#edge:<id>` or `#group:<id>`
- `page:<page-id>#section:<id>` or `#block:<id>`

Run validation to get the authoritative missing-reference list. Update the plan and manifest until
`missing` is empty, then register it:

```bash
python3 .agentflow/scripts/agentflow.py plan validate --input /tmp/plan.json
python3 .agentflow/scripts/agentflow.py plan register --input /tmp/plan.json
```

Registration is immutable. Do not register an incomplete draft.

## Reconcile Before Finishing

- Every required source reference appears exactly once.
- Every plan task has at least one approved source reference.
- Every technical action is justified by architecture, not convenience.
- No approved branch, failure, async boundary, persistence rule, or outcome is missing.
- Verification covers each represented behavior.
- The Markdown plan and manifest describe the same tasks.

Report the plan path, plan ID, proposal ID, architecture hash, and validation coverage. Do not begin
implementation unless the user requested it.
