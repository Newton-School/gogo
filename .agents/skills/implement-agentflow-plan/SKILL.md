---
name: implement-agentflow-plan
description: Implement a registered AgentFlow plan, map every change to approved tasks, and complete the architecture only after an exact implementation audit passes.
---

# Implement AgentFlow Plan

Use this skill when the user asks to implement a plan registered from approved AgentFlow
architecture.

## Contract

Implement every plan task and nothing outside the plan. Existing repository rules still control
code quality, security, testing, and conventions, but they cannot expand product scope. When the
code requires an architecture difference, stop and create a new proposal.

## Prepare

1. Read repository instructions, relevant memories, the registered plan Markdown, and its manifest.
2. Read the exact current architecture identified by the plan's `architectureHash`.
3. Inspect existing code and record the starting Git state so unrelated user changes stay separate.
4. Confirm the plan still targets the latest current architecture:

```bash
python3 .agentflow/scripts/agentflow.py status
python3 .agentflow/scripts/agentflow.py plan list
```

## Implement In Plan Order

- Keep each change attributable to one or more plan task IDs.
- Implement complete paths, including decisions, permissions, failures, persistence, async work,
  integrations, and terminal outcomes represented by the plan.
- Run focused verification as each task is completed, then the relevant broader checks.
- Do not opportunistically add nearby features, fields, API responses, jobs, or refactors.
- If a required non-behavioral support change is necessary for the approved task, include it in that
  task's evidence and explain the necessity.

## Audit The Complete Diff

Inspect every file changed by this implementation, not only files you expected to touch. Preserve
pre-existing unrelated changes and exclude them from AgentFlow evidence. For each implementation
file, migration, test, configuration, runtime check, or necessary documentation change, map it to
the plan tasks it proves.

Create a temporary evidence file:

```json
{
  "schemaVersion": 3,
  "planId": "add-invite-delivery",
  "architectureHash": "<current architecture hash>",
  "items": [
    {
      "taskId": "persist-delivery-attempts",
      "status": "implemented",
      "summary": "Added the approved model, migration, and constraints."
    }
  ],
  "changes": [
    {
      "path": "backend/tenancy/models.py",
      "kind": "code",
      "symbol": "InviteDeliveryAttempt",
      "taskIds": ["persist-delivery-attempts"],
      "evidence": "Model fields and constraints match the approved table page."
    },
    {
      "path": "backend/tenancy/tests/test_invite_delivery.py",
      "kind": "test",
      "taskIds": ["persist-delivery-attempts"],
      "evidence": "Covers creation and uniqueness behavior."
    }
  ]
}
```

Allowed evidence kinds are `code`, `migration`, `test`, `config`, `runtime`, and `documentation`.
Every plan task must appear exactly once in `items`. Every implemented task needs at least one
`changes` entry. A blocked task remains `blocked` with a concrete summary.

Audit before completion:

```bash
python3 .agentflow/scripts/agentflow.py implementation audit --input /tmp/evidence.json
```

Also reconcile manually:

- every plan task is implemented or explicitly blocked;
- every changed file or runtime operation made for this work maps to a task;
- every approved architecture reference is represented through the registered plan;
- no behavior outside the architecture and plan appears in the diff;
- verification results support the evidence summaries.

Fix all mismatches. Do not mark implementation complete while any task is blocked. Once the audit
is clean, persist evidence and mark the current architecture hash as implemented:

```bash
python3 .agentflow/scripts/agentflow.py implementation complete --input /tmp/evidence.json
```

## Finish

Update memory only for stable repository conventions or durable architecture preferences. Report
the implemented architecture hash, plan task coverage, key verification results, and any work that
could not be completed. Never claim the approved architecture is implemented before
`implementation complete` succeeds.
