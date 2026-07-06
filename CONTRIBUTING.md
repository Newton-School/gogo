# Contributing To Gogo

Thanks for helping improve Gogo. This project is a Go backend framework with
Django-style apps, models, migrations, admin, auth, APIs, forms, templates,
static files, contrib packages, and Celery-style queues.

## Source Of Truth

- Read `AGENTS.md` before changing code. It is the base guide for both humans
  and AI agents.
- Read the matching files in `.agent/rules/` for the workflow you are touching.
- Read the relevant `.plans/` file when implementing planned framework
  behavior.
- Keep generated client projects on public framework packages. Generated
  projects must not import `github.com/Newton-School/gogo/internal`.

## Development Setup

Use Go `1.26.4` or newer.

```bash
go mod download
make test
```

For local framework configuration, copy `.env.example` to `.env`. Never commit
`.env`, credentials, local databases, generated uploads, private keys, tokens,
or machine-specific paths.

## Common Commands

```bash
make test
make lint
make docs-verify
make build
```

Before opening a broad framework change, run:

```bash
make ci
go test -tags=integration ./...
```

For queue, ORM, HTTP, session, cache, signal, or CLI concurrency work, also run
the focused race target:

```bash
make race-concurrency
```

If `govulncheck` is not installed, `make ci` skips the local vulnerability
scan. Report that skip in the contribution notes.

## External Integration Tests

Some integration tests need external services and are skipped when the matching
environment variables are absent:

- PostgreSQL: `GOGO_TEST_POSTGRES_DSN`
- Redis: `GOGO_TEST_REDIS_ADDR`
- RabbitMQ: `GOGO_TEST_RABBITMQ_URL`

Say which external integrations were not exercised when reporting verification.

## Change Requirements

- Keep changes tightly scoped to the requested behavior.
- Follow existing package boundaries and local patterns.
- Prefer the standard library unless a dependency clearly reduces risk.
- Add or update tests for behavior changes.
- Update docs when public behavior, commands, generated files, configuration,
  compatibility, or release behavior changes.
- Keep `.env.example` and generated project env templates synchronized when
  environment variables change.
- Preserve backward compatibility for exported APIs unless a breaking change is
  deliberate and documented.

## Public API And Compatibility

Public packages are part of the framework contract. Before changing exported
types, functions, generated project structure, migration behavior, auth/session
semantics, queue contracts, or CLI command behavior:

- Check `.agent/rules/public-api-compatibility.md`.
- Add compatibility tests when the change can affect users.
- Document migration steps for breaking changes.
- Add changelog notes for user-visible changes.

## Security

Report vulnerabilities privately through GitHub Security Advisories:

https://github.com/Newton-School/gogo/security/advisories/new

Do not open public issues, pull requests, discussions, or chat threads for
unpatched vulnerabilities. Follow `SECURITY.md` for required report content,
secret handling, and disclosure expectations.

## Pull Request Checklist

Before submitting a pull request:

- The change is scoped and does not include unrelated refactors.
- Tests were added or updated where behavior changed.
- Documentation was updated for user-visible behavior.
- Generated docs were checked when public package lists or examples changed.
- No secrets, local files, generated uploads, or machine-specific paths are
  committed.
- Verification commands and any skipped coverage are listed.

## Release Process

Releases are maintainer-only and manual. Do not create release tags or GitHub
Releases unless explicitly asked by a maintainer.

When asked to publish a release, follow `.agent/rules/ci-release.md`: inspect
changes since the previous release, choose the next semantic version, update
`CHANGELOG.md`, verify, create the annotated tag, push it, and confirm the
GitHub Release artifacts.

## License

By contributing, you agree that your contributions are licensed under the MIT
License in `LICENSE`.
