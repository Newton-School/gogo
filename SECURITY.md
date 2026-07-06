# Security Policy

## Supported Versions

Gogo is pre-1.0 software until the first stable release is tagged in
`github.com/Newton-School/gogo`. Security fixes are made against the current
development branch and included in the next available release.

| Version | Security support |
| --- | --- |
| Current development branch | Supported before public release. |
| Latest tagged 0.x release | Best effort. Upgrade to the latest tag or commit when a fix is published. |
| Older 0.x tags | Unsupported unless a maintainer explicitly announces a backport. |

After `v1.0.0`, the latest minor release is supported for security fixes. The
previous minor release may receive critical fixes when the patch can be applied
without changing public APIs, generated project layout, or migration semantics.

## Reporting A Vulnerability

Report suspected vulnerabilities through GitHub Security Advisories:

https://github.com/Newton-School/gogo/security/advisories/new

Do not open a public issue, pull request, discussion, or chat thread for an
unpatched vulnerability. Include:

- Affected Gogo version, commit, or generated project version.
- Affected package or command.
- Reproduction steps, proof of concept, or failing test.
- Expected impact and any known workarounds.
- Whether the issue is already public or under active exploitation.

Do not include production secrets, private keys, database dumps, customer data,
session cookies, password reset links, or access tokens in the report. Redact
those values before sharing logs or screenshots.

## Response Timeline

The project aims to follow this timeline for private reports:

- Acknowledge the report within 3 business days.
- Triage severity and affected versions within 7 business days.
- Share the planned fix, mitigation, or rejection reason after triage.
- Publish a fix as soon as practical for confirmed high or critical issues.
- Coordinate advisory publication after a fix, workaround, or explicit upgrade
  path is available.

Timelines may change when a report needs third-party coordination, a Go
standard library fix, a database driver fix, or additional reproduction data.

## Secret Handling

Security fixes and tests must not commit real credentials. Use placeholders in
examples and store runtime values in environment variables or secret managers.
The `.env` file is for local development and must stay ignored by Git.

Rotate credentials immediately if they are exposed in a report, log, build
artifact, CI output, issue, pull request, or release asset. Treat leaked
`GOGO_SECRET_KEY` values as session-signing and token-signing compromise.

## Dependency Updates

Dependencies are audited with `go list -m all` and vulnerability scanning in
CI. Maintainers should prioritize updates for:

- Go security releases.
- Database drivers and wire protocol packages.
- Password hashing and cryptographic packages.
- Queue, cache, and broker clients.
- Transitive dependencies with reachable vulnerabilities.

Security updates should be small, reviewed, and verified with the full test
suite, generated project checks, examples, and documentation verification.

## Disclosure Process

Confirmed vulnerabilities are disclosed through a GitHub Security Advisory and
release notes. Public disclosure should include the affected versions, severity,
impact, patched version or commit, migration or upgrade instructions, and known
workarounds. When appropriate, the project can request a CVE through GitHub's
advisory workflow.
