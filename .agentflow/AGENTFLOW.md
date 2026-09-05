---
name: agentflow
description: Start and build software projects through a local architecture review gate. Use when a user invokes AgentFlow, asks to initialize a project with AgentFlow, or works in a repository that already contains `.agentflow/`.
---

# AgentFlow

AgentFlow keeps the user in one conversation with their normal AI agent. Operate the harness for
them. Do not ask the user to install a package, author JSON, or run lifecycle commands.

## Start A Project

When `.agentflow/config.json` does not exist, inspect the repository before bootstrap. Determine the
stack from source files and configuration, not only from the user's description. A repository is
Django when evidence such as `manage.py` with `DJANGO_SETTINGS_MODULE`, active settings with
`INSTALLED_APPS`, and a Django dependency is present. Use `generic` when no supported stack profile
is proven. Future stack profiles follow the same evidence-based rule.

Run bootstrap after inspection:

```bash
python3 <skill-directory>/scripts/bootstrap.py \
  --project <project-root> \
  --name "<project-name>" \
  --id <stable-project-id> \
  --description "<short-description>"
```

Bootstrap uses only the latest published harness. It creates `.agentflow/`, `.agents/`, compatibility
links for Claude, the AgentFlow instruction block in `AGENTS.md`, and `make agentflow`. It preserves
existing repository instructions. It independently verifies the detected stack, installs only the
matching client skill set, and records `harnessProfile` in `.agentflow/harness.json`. Use
`--profile <name>` only when repository evidence requires an explicit override. After bootstrap,
use the repo-local harness; this source repository is not a runtime dependency.

When AgentFlow already exists, read `.agentflow/AGENTFLOW.md`, relevant `.agents/rules/`, memories,
repo-local skills, and `.agentflow/harness.json` for the installed profile. If the architecture
schema is older, report that migration is required. Do not update, migrate, or switch a client
profile unless the user explicitly asks.

For an explicitly requested harness update, use `$update-agentflow-harness` and the client-local
migration lifecycle. If a client has `.agentflow/legacy-v3/`, inspect the official
`migrate --rebuild-from-v3 --check` path before any correction. Never edit generated architecture
or proposal JSON by hand to repair migration navigation.

## Decide Whether Architecture Review Is Required

Create a proposal before work that changes user or system behavior, data shape, APIs, permissions,
jobs, queues, events, integrations, or cross-module contracts. Copy changes, visual styling,
isolated refactors, and narrow fixes that leave the represented architecture unchanged can proceed
without an empty proposal; state why the approved architecture is unaffected.

Never use AgentFlow as a specification workflow. Its job is to preserve the minimum accurate
architecture developers need to review and the exact scope agents must implement.

## Build Architecture

Use `.agents/skills/create-update-agentflow-architecture/SKILL.md` and
`.agentflow/references/proposal-format.md`.

The installed repo-local skill owns stack-specific hierarchy rules. Under the generic profile, the
client owns all navigation names, nesting, and ordering; do not force apps, features, Models, Flows,
or any other folder. Never weaken a stack profile's hierarchy constraints. The only AgentFlow page
types are:

- `table`: structured records such as models, fields, enums, permissions, or configuration.
- `flow`: a complete technical path shown as a simple flowchart.
- `document`: concise context that cannot be expressed clearly as a table or flow.

Flow nodes must use AgentFlow's fixed types: `actor`, `interface`, `action`, `api`, `decision`,
`database`, `cache`, `queue`, `job`, `event`, `external`, `subprocess`, `terminal`, or `error`.
Do not invent client-specific visual types. Flows always run vertically from top to bottom, with
parallel branches placed side-by-side. Put exact technical names in subtitles and details while
keeping node titles readable.

Work from inspected code and authoritative requirements. Read the current architecture and inspect
whether it is already implemented:

```bash
python3 .agentflow/scripts/agentflow.py status
python3 .agentflow/scripts/agentflow.py snapshot --state current
```

`status` reports `architectureHash`, `implementedArchitectureHash`, and whether implementation is
pending. `snapshot --state implemented` is available only when those hashes match; AgentFlow keeps
one current architecture rather than historical full-page snapshots.

Create, validate, and submit the proposal, then regenerate the index without opening it:

```bash
python3 .agentflow/scripts/agentflow.py proposal create --input /tmp/proposal.json
python3 .agentflow/scripts/agentflow.py validate --proposal <proposal-id>
python3 .agentflow/scripts/agentflow.py proposal submit <proposal-id>
make agentflow PROPOSAL=<proposal-id>
```

For an existing active proposal, use `proposal revise`. Tell the user to refresh their existing tab
or manually open `.agentflow/index.html`. Never open the page while authoring or revising unless the
user explicitly asks.

## Apply Comments

Use `.agents/skills/apply-agentflow-comments/SKILL.md`. Reviewer comments are saved as Open against
the selected page, row, cell, node, edge, section, block, or navigation item. Always import and read
every Open comment on the selected proposal before revising. Resolve a comment only when the
validated architecture visibly contains the requested change.

Update `.agents/memory/architecture-feedback.md` only when feedback is durable enough to guide
future architecture. Do not turn every comment into memory.

## Require Explicit Approval

Approval must be explicit and must refer to the submitted architecture. Do not infer approval from
silence, positive feedback, or a general request to continue. Approval applies the submitted
proposal to the current architecture and records an immutable proposal review; it does not claim
that code already matches it:

```bash
python3 .agentflow/scripts/agentflow.py proposal approve <proposal-id> --approved-by user
```

`--implemented-baseline` is reserved for initial documentation of code that was already inspected
and exists. Do not use it for new work.

Agents may prepare and submit later proposals while implementation is pending, but must not approve
another one until the current architecture is implemented. This keeps every plan cumulative and
prevents earlier approved scope from disappearing.

## Plan And Implement Exactly

After approval, use `.agents/skills/create-agentflow-plan/SKILL.md`. Register a structured plan
manifest whose `sourceRefs` cover every changed architecture element. The CLI rejects missing or
unapproved references.

Use `.agents/skills/implement-agentflow-plan/SKILL.md` for implementation. Audit every plan task,
all changed files and runtime evidence, and any blocked work. Only a complete audit updates
`implementedArchitectureHash` to the current architecture hash. If code requires a different
architecture, stop and create another proposal rather than silently changing the plan.

## Invariants

- Do not create or use `.specs/`.
- Do not install an AgentFlow package, browser dependency, remote service, or application server.
- `.agentflow/index.html` is a generated local file. Only the copied standard-library loopback
  comment writer may run locally.
- Keep the current architecture canonical; approved proposal records and submitted versions are
  immutable. Operate architecture, proposal, and comment state only through the CLI.
- Keep credentials, real tokens, customer data, and private personal information out of the
  harness.
- Preserve repository rules, memories, architecture, and repo-specific skills.
- Commit harness source files but not generated files excluded by `.agentflow/.gitignore`.
