# Gogo

A proposed Go backend framework with structured applications, its own ORM, an optional Admin module, and an optional Async task system. PostgreSQL and Redis are the initial external data backends.

## Architecture review

Architecture is maintained only through AgentFlow. The framework is **not implemented yet**, and the submitted design is not approved.

The review contains 128 feature flowcharts and 24 structured catalogs under Core, Admin, Async, Connectors, and Developer Tools and Operations. Start with **Review map → Developer journey**, then explore the owning feature trees. Nodes expose technical details; catalogs enumerate fields, options, interfaces, settings, and verification requirements.

The local review entrypoint is `.agentflow/index.html`. Regenerate it with:

```sh
make agentflow PROPOSAL=gogo-complete-framework-architecture
```

This renders the review and enables its project-local comment writer without opening a browser. Generated HTML and local runtime credentials are ignored by Git. Changes to architecture go through proposals and explicit review approval before framework implementation.
