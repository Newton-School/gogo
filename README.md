# Gogo

A Go backend framework under development with structured applications, its own ORM, an optional Admin module, and an optional Async task system. PostgreSQL and Redis are the initial external data backends.

## Architecture review

Architecture is maintained only through AgentFlow. Architecture version 2 is approved; implementation is in progress under the registered `gogo-framework-end-to-end` plan. Approval does not mean the framework is implemented or ready for production.

The review contains 128 feature flowcharts and 24 structured catalogs under Core, Admin, Async, Connectors, and Developer Tools and Operations. Start with **Review map → Developer journey**, then explore the owning feature trees. Nodes expose technical details; catalogs enumerate fields, options, interfaces, settings, and verification requirements.

The local review entrypoint is `.agentflow/index.html`. Regenerate it with:

```sh
make agentflow PROPOSAL=gogo-complete-framework-architecture
```

This renders the review and enables its project-local comment writer without opening a browser. Generated HTML and local runtime credentials are ignored by Git. Changes to architecture go through proposals and explicit review approval before framework implementation.

## Contributor checks

Run `make test` for workspace race tests and Admin JavaScript contract tests.
The latter use Node.js 20+ built-ins only, with no npm dependencies or browser;
`make test-js` runs them separately. Node.js is a contributor test dependency,
not a dependency of installing, building or running any public Go module.

`make test-modules` verifies the six public modules with workspace overrides
disabled and builds a fresh generated consumer. `make test-integration`
requires supported PostgreSQL and Redis tooling and uses owned disposable
fixtures; ordinary tests may explicitly skip unavailable local services.
Run `make vet build` for the workspace static and compilation checks.
