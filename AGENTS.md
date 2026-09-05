# Gogo root agent harness

## Active workflow: AgentFlow

The owner selected AgentFlow for Gogo architecture review. The AgentFlow block below and `.agentflow/AGENTFLOW.md` govern architecture, approval, planning, and implementation. The older `.specs/` routing and specification skills retained below are inactive for this project; do not create, read, or update `.specs/` as part of AgentFlow work. Preserve the previous scaffold and skills unless removal is explicitly requested.

Gogo is a greenfield Go framework. Its proposed product roots are `core/`, `admin/`, `async/`, and `connectors/`, with private implementation in `internal/`. PostgreSQL and Redis are the initial external backends. Keep future backend contracts public, and keep Admin and Async independently installable. Proposed architecture is not implemented code.

Use feature-parent navigation trees, detailed vertical flowcharts, and structured tables. Every material branch, data effect, error, asynchronous handoff, and terminal outcome must be visible. Keep prose brief. Do not open the generated AgentFlow page unless the owner explicitly asks.

Work on the current branch. Use the already-open Chrome browser when browser work is explicitly requested; do not use Playwright or the in-app browser unless requested. Keep secrets and local paths out of committed code and configuration, group ignore/environment files by purpose, and validate required configuration without embedding secrets into binaries.

## Retained prior workflow (inactive)

This root harness applies to the repository, including `backend/`, `frontend/`, `.specs/`, tests, scripts, and `deployment/`. Do not add nested `AGENTS.md` files unless the repository intentionally requires scoped harnesses.

## Required reading order

Before planning or changing product behavior:

1. Read this file completely.
2. Read every Markdown file under `.agents/rules/` in lexical order when that directory exists.
3. Read every applicable repository skill completely.
4. Read the smallest complete set of relevant generated `.specs/*.html#anchor` clauses.
5. Inspect current code, schemas, migrations, generated artifacts, tests, configuration, infrastructure, and repository state.

Do not begin from assumptions or treat current code behavior as intended when `.specs/` defines the contract.

## Repository skills

- `.agents/skills/maintain-specifications/SKILL.md` — use automatically for every specification task and whenever planning or implementation discovers or changes a product, data, interface, event/job, permission, provider, runtime, migration, client, security, acceptance, or documentation contract.
- `.agents/skills/create-feature-plan/SKILL.md` — use for feature/change planning and before non-trivial behavior-affecting implementation.
- `.agents/skills/implement-feature/SKILL.md` — use for end-to-end feature implementation; it must use the planning skill and the specification skill.

## Product and architecture authority

- `.specs/` is the sole authority for intended product behavior and system design. Code and tests show implementation state but do not override it.
- Cite exact generated `.specs/*.html#anchor` clauses in plans and conformance evidence.
- Edit specification source modules and regenerate HTML. Never hand-edit generated pages.
- Preserve the bundled Stride documentation shell and repository-backed `.specs/_inventory.py` coverage. Every automatically discovered runtime source unit and declared entry flow must have one reciprocal focused owner page; the only theme switcher belongs in the top-right bar.
- If required behavior or architecture is missing, contradictory, or changed, stop dependent work and follow `maintain-specifications`: explain impact, ask the focused owner question, update specs first, regenerate, validate, rate, then plan or implement.
- Recheck every consumer when shared data, interface, event/job, permission, runtime, provider, migration, client, or security contracts change.

## Working state

- Keep implementation plans under ignored `.plans/`; plans are execution state, not requirements.
- Work on the current branch unless the user explicitly requests another branch/worktree.
- Preserve unrelated changes and stage only owned files.
- Commit small, coherent, verified checkpoints when commits are in scope.
- Do not push, open pull requests, rewrite history, or run destructive Git commands without explicit authorization.

## Engineering baseline

- Treat the product as production-grade. Enforce authorization and scope at trusted boundaries for reads, commands, jobs, exports, notifications, and private assets.
- Keep domain decisions in their specified owner; UI visibility is not authorization and transport code is not domain truth.
- Validate untrusted input and provider callbacks, keep secrets and sensitive data out of clients/logs/fixtures/commits, and use safe stable errors.
- Preserve specified history, versioning, audit, concurrency, idempotency, retries, compatibility, rollback, and recovery.
- Regenerate generated artifacts from source and run meaningful success, denial, failure, retry, concurrency, recovery, accessibility, security, and regression checks.
- Do not claim software is bug-free; report concrete checks, risks, and true blockers.

## Definition of complete

A feature or plan is complete only when its skill workflow finishes, every applicable test and generation gate passes, migrations/generated artifacts are current, affected specifications and consumers are reconciled, every conformance row is resolved, and no accidental or sensitive file remains.

<!-- agentflow:start -->
## AgentFlow Harness

This repository uses AgentFlow as the local agent harness for architecture review, planning, and implementation control.

Before changing product behavior, data models, APIs, permissions, events, background jobs, integrations, or cross-module contracts:

1. Read relevant files in `.agents/rules/` and `.agents/memory/`.
2. Use the matching repo-local skill in `.agents/skills/`.
3. Read `.agentflow/AGENTFLOW.md` for lifecycle commands.
4. Create or update table, flow, or document pages in an AgentFlow proposal and render the client-owned hierarchy with `make agentflow PROPOSAL=<proposal-id>`.
5. When the user asks to fix, address, resolve, or review AgentFlow comments, use `.agents/skills/apply-agentflow-comments/SKILL.md`.
6. Do not implement until the user explicitly approves the submitted architecture.

After approval, register a traceable plan that covers every changed architecture reference, then implement only that plan. Complete implementation evidence before claiming the approved architecture exists in code. If code work requires a different architecture, stop and create a new AgentFlow proposal first.

Keep durable review feedback in `.agents/memory/architecture-feedback.md` so future architecture proposals do not need the same comments again.
<!-- agentflow:end -->
