# Gogo root agent harness

## Active workflow: AgentFlow

Use only AgentFlow for architecture, approval, planning, and implementation. Treat Gogo as a new product defined by the owner's current requirements. Do not create or use a separate specification workflow.

Gogo is a greenfield Go framework. Its proposed product roots are `core/`, `admin/`, `async/`, and `connectors/`, with private implementation in `internal/`. PostgreSQL and Redis are the initial external backends. Keep future backend contracts public, and keep Admin and Async independently installable. Proposed architecture is not implemented code.

Use feature-parent navigation trees, detailed vertical flowcharts, and structured tables. Every material branch, data effect, error, asynchronous handoff, and terminal outcome must be visible. Keep prose brief. Do not open the generated AgentFlow page unless the owner explicitly asks.

Work on the current branch. Use the already-open Chrome browser when browser work is explicitly requested; do not use Playwright or the in-app browser unless requested. Keep secrets and local paths out of committed code and configuration, group ignore/environment files by purpose, and validate required configuration without embedding secrets into binaries.

## Working state

- Use the repo-local AgentFlow lifecycle for proposals, comments, plans, and evidence.
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

A feature is complete only when its approved AgentFlow plan and implementation audit are complete, applicable tests pass, generated artifacts are current, and no accidental or sensitive file remains. An architecture proposal is ready for review when its feature tree, concrete flows, structured contracts, and validation cover the requested scope.

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
