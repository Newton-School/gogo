# Gogo contributor instructions

## Project scope

Follow the owner's current requirements. Inspect existing code before implementation and keep changes scoped. Work directly in source, tests and documentation; do not introduce a separate specification workflow.

Gogo is a Go framework with product roots `core/`, `admin/`, `async/`, and `connectors/`, and private implementation in `internal/`. PostgreSQL and Redis are the initial external backends. Keep future backend contracts public, and keep Admin and Async independently installable. Distinguish implemented behavior from proposed work.

Work on the current branch. Use the already-open Chrome browser when browser work is explicitly requested; do not use Playwright or the in-app browser unless requested. Keep secrets and local paths out of committed code and configuration, group ignore/environment files by purpose, and validate required configuration without embedding secrets into binaries.

## Working state

- Work on the current branch unless the user explicitly requests another branch/worktree.
- Preserve unrelated changes and stage only owned files.
- Commit small, coherent, verified checkpoints when commits are in scope.
- Do not push, open pull requests, rewrite history, or run destructive Git commands without explicit authorization.

## Quality and verification

- Enforce authorization in backend code, validate untrusted input, and keep secrets out of source, logs and generated output.
- Preserve API compatibility and existing data unless the requested change explicitly requires otherwise.
- Run relevant tests and regenerate affected documentation from its source. Report actual results and any unverified behavior.
- Use `make help` for the available build, test and documentation commands.
