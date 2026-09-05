# AgentFlow Harness Rules

These rules apply to architecture, planning, and implementation work in this repository.

## Load Order

1. Read `AGENTS.md` and relevant repository instructions.
2. Read relevant files in `.agents/rules/` and `.agents/memory/`.
3. Use the matching repo-local skill in `.agents/skills/`.
4. Use `.agentflow/AGENTFLOW.md` and `.agentflow/scripts/agentflow.py` for lifecycle operations.

## Architecture Gate

- Propose architecture before changing product behavior, data shape, APIs, permissions, jobs,
  queues, events, integrations, or cross-module contracts.
- Do not implement until the user explicitly approves the submitted architecture.
- Approval and implementation are different states. Approval updates the current architecture, and
  it becomes implemented only after its registered plan and implementation evidence pass the audits.
- Do not approve a later proposal while the current architecture is pending implementation.
- When implementation requires an architectural difference, stop and propose that difference.
- Do not create or use a `.specs/` workflow for AgentFlow work.

## Architecture Contract

- The client controls navigation names, nesting, and ordering. Do not impose app or feature folders.
- Use only `table`, `flow`, and `document` pages.
- Prefer tables for structured definitions and flows for behavior. Use documents sparingly.
- Use only the fixed AgentFlow flow-node palette. Never invent a visual node type.
- Keep flowcharts as readable as a hand-drawn technical flowchart: short labels, explicit branches,
  complete outcomes, and technical details in the inspector rather than dense node text.
- Every flow must include the real boundaries needed to understand it, including entry, calls,
  decisions, persistence, asynchronous work, integrations, failures, and terminal outcomes.
- Use stable IDs for navigation items, pages, table columns and rows, flow nodes and edges, and
  document sections and blocks. Comments and plan coverage depend on them.

## Scope Discipline

- Read existing code as well as requirements before drawing current or proposed architecture.
- `implementedArchitectureHash` describes code that exists. The current architecture may describe
  accepted work that is still pending implementation. Never present one as the other.
- Keep exact routes, symbols, tables, queues, tasks, events, constraints, and service names in page
  details where they matter to implementation.
- A plan must cover every changed architecture reference and no unrelated source reference.
- Implementation evidence must account for every plan task and every changed artifact. Inspect the
  complete code diff so unplanned behavior is not silently included.
- Preserve existing repository rules, memories, local skills, and architecture state.

## Comments And Memory

- Import the comment inbox and read every Open comment on the selected proposal before revising.
- Resolve a comment only after its requested change is visible in the validated proposal.
- Preserve Resolved comments as immutable review history.
- Store only durable architecture preferences in `.agents/memory/architecture-feedback.md` and
  stable repository facts in `.agents/memory/project-context.md`.
- Keep secrets, real tokens, customer data, and private personal information out of AgentFlow.

## Generated Page

- `make agentflow` regenerates `.agentflow/index.html` and never needs an application server.
- Do not open the page automatically while working. The user can refresh an existing tab or open it
  manually.
- The standard-library loopback writer may be started only by `make agentflow` to save comments
  inside the current project's `.agentflow/comment-inbox/`.
