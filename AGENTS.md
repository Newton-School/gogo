# Agent Guide

This file is the source of truth for AI agents working on Gogo. `CLAUDE.md` must point to this file so every agent reads the same guidance.

## Product Context

Gogo is a Go backend framework with Django-style apps, models, migrations, admin, auth, APIs, forms, templates, static files, contrib apps, and Celery-style queues.

Primary implementation plans live in `.plans/`. Current framework code is organized by public packages such as `app`, `http`, `models`, `orm`, `migrations`, `auth`, `admin`, `api`, `forms`, `templates`, `files`, `static`, `queue`, `cache`, `email`, `messages`, `security`, `sessions`, `signals`, `i18n`, `health`, `observability`, `contrib`, and `testing`. Internal implementation belongs under `internal/`.

## Base Rules

- Work on the current branch. Do not create branches or worktrees unless explicitly requested.
- Inspect the relevant code, tests, docs, and plan before editing.
- Keep changes tightly scoped to the requested workflow.
- Do not revert user changes or unrelated work.
- Use public framework packages for generated client projects. Generated projects must not import `github.com/Newton-School/gogo/internal`.
- Keep `.env.example` and generated `internal/cli/templates/project/env.example.tmpl` synchronized when environment variables change.
- Never commit `.env`, local databases, credentials, tokens, generated uploads, local media, or machine-specific paths.
- Prefer standard library code unless an added dependency materially reduces risk. Document dependency intent when adding one.
- Update tests and docs with behavior changes.
- Verify before claiming completion.

## Rule Routing

Read the specific rule files that match the task:

| Task Area | Rule File |
| --- | --- |
| Repository layout and package boundaries | `.agent/rules/repo-structure.md` |
| Branching, commits, and edit workflow | `.agent/rules/development-workflow.md` |
| Plan-driven implementation and scope control | `.agent/rules/planning-and-scope.md` |
| Required verification commands | `.agent/rules/testing-verification.md` |
| Secrets, env, auth, sessions, CSRF, deploy safety | `.agent/rules/security-config-env.md` |
| Exported APIs and compatibility | `.agent/rules/public-api-compatibility.md` |
| CLI commands and generated project/app templates | `.agent/rules/cli-and-generators.md` |
| Models, ORM, and migrations | `.agent/rules/models-orm-migrations.md` |
| HTTP, admin, API, and auth workflows | `.agent/rules/http-admin-api-auth.md` |
| Queue workers, beat, brokers, and canvas | `.agent/rules/queue-workers.md` |
| Forms, templates, static files, files, fixtures | `.agent/rules/templates-forms-static-files.md` |
| Django contrib-style packages | `.agent/rules/contrib-apps.md` |
| Docs, tutorials, examples, generated docs | `.agent/rules/docs-examples.md` |
| GitHub Actions, manual release publishing, changelog, dependencies | `.agent/rules/ci-release.md` |
| Final audit and review pass | `.agent/rules/audit-review.md` |

## Default Verification

For most code changes, run the smallest relevant test first, then broaden as risk increases.

Common gates:

```bash
go test ./...
go test -tags=integration ./internal/cli
make docs-verify
```

Before release, generated-project, queue, ORM, migration, auth, admin, or public API changes, prefer:

```bash
make ci
go test -tags=integration ./...
go test -race ./queue/... ./orm/... ./http/...
```

If a command cannot run, report the exact reason and the risk left unverified.
