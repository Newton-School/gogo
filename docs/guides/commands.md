# Management commands

Use the standalone `gogo` executable to bootstrap a project. Once the project exists, use `go run manage.go …` or its compiled binary. This selects your app registry, settings and optional services.

## Command map

| Command | Purpose | Integration requirement |
| --- | --- | --- |
| `help`, `version` | Discover available commands and framework version | Core |
| `diffsettings` | Inspect resolved settings with secrets redacted | Project schema |
| `startproject NAME --module PATH` | Create a project pinned to the framework version | Core |
| `startapp LABEL` | Create and register an app | Existing generated project |
| `generate`, `generate --check` | Write or verify generated model descriptors | App model source |
| `check` | Run configured project checks | Relevant configuration must validate |
| `build` | Run the framework's project build path | Generated project and required configuration |
| `test`, `test --race` | Run project Go tests | Existing Go client |
| `runserver`, `serve` | Run the explicitly configured HTTP handler | Project Handler and runtime resources |
| `runserver --reload` | Supervise rebuild/restart during development | Linux/macOS; never production |
| `makemigrations APP` | Create model migration source | Registered model/migration contributions |
| `migrate`, `showmigrations`, `sqlmigrate APP NAME` | Apply/reverse, inspect history, preview SQL | Registered migration commands and database |
| `inspectdb` | Introspect an explicitly configured database | Registered inspection command |
| `dumpdata`, `loaddata` | Export/import fixtures | Explicit fixture factories and authorization |
| `collectstatic`, `findstatic` | Collect or locate static assets | Explicit static commands and sources |
| `openapi` | Write the declared OpenAPI schema | Explicit schema factory |
| `worker`, `beat`, `tasks`, `queues` | Run workers/scheduler and scoped task/queue operations | Registered optional Async factories |
| `task-child` | Internal subprocess task IPC entrypoint | Explicit registered child factory; not an end-user job command |

Use `go run manage.go COMMAND --help` for exact flags. Commands requiring factories are not activated by importing a package, and sample-specific `seed`, `createadmin`, `fields` and `demoasync` are not universal built-ins.

## Write a custom command

Add a `management.Command` to `Project.Commands`. `Configure` declares flags and returns the runner. `Validate` checks positional arguments. `Resources` identifies required configuration, while `OpenResources` requests actual resource startup.

Use `RequiredFlags` when a flag must be explicitly provided even if a default exists. Write to `Invocation.Stdout`/`Stderr`, respect the runner's context, and return errors. Do not call `os.Exit` inside a callback; the lifecycle owner still needs to close resources.

For commands that can mutate data, make write intent explicit, validate targets before writes, and keep progress output free of credentials and customer records.

## Checks and generated artifacts

`core/checks.Registry` holds named checks. Descriptor checks and migration-drift checks address different problems; run both after changing models. Check in migration snapshots, registries and generated references. A clean compile does not prove schema compatibility with a deployed database.

## Error handling

The command layer reports stable configuration/command failures and a nonzero exit status. Raw provider errors are not a public CLI contract. Automation must check the exit status, not infer success from a partially printed message. For fixture commands, consult the detailed transfer/receipt contract before retrying a failed import.
