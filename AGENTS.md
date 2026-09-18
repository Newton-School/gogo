# Gogo contributor instructions

## Project scope

Follow the owner's current requirements. Keep changes scoped, inspect existing code before implementation, and do not introduce a separate specification workflow.

Gogo is a Go framework with product roots `core/`, `admin/`, `async/`, and `connectors/`, and private implementation in `internal/`. PostgreSQL and Redis are the initial external backends. Keep future backend contracts public, and keep Admin and Async independently installable. Distinguish implemented behavior from proposed work.

Use feature-parent navigation trees, detailed vertical flowcharts, and structured tables when documenting architecture. Every material branch, data effect, error, asynchronous handoff, and terminal outcome must be visible. Keep prose brief.

Work on the current branch. Use the already-open Chrome browser when browser work is explicitly requested; do not use Playwright or the in-app browser unless requested. Keep secrets and local paths out of committed code and configuration, group ignore/environment files by purpose, and validate required configuration without embedding secrets into binaries.

## Working state

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

A feature is complete only when the requested behavior is implemented and reviewed, applicable tests pass, generated artifacts are current, and no accidental or sensitive file remains. Report remaining limitations and unverified behavior explicitly.
