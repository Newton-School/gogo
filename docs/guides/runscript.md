# Runscript

Run a trusted Go file using your application's source and dependencies. `runscript` is a built-in management command; `gogo.Script` is a helper in the existing root import. No extra module, interpreter, package installation, or environment variable is required.

This feature is available in this source checkout, not the previously published `v1.0.0-alpha.1`. Upgrade to a release containing it before using these examples in a separately installed client.

## Run a file

From your project root, with a locally installed Go toolchain and dependencies already downloaded:

```sh
go run manage.go runscript scripts/maintenance.go
```

The compiled management binary supports the same command:

```sh
./manage runscript scripts/maintenance.go --timeout=2m
```

The input is one regular `.go` file containing `package main` and `func main()`. Its path must be relative to and inside `Project.Root` (the current directory by default); absolute paths, escaping symlinks, directories, and `_test.go` files are rejected. Sibling files in the script directory are not compiled automatically. Put shared helpers in an importable application package.

## Load your project

Create `scripts/maintenance.go`. Replace `example.com/shop` with the module path in your `go.mod`:

```go
package main

import (
    "context"
    "fmt"
    "example.com/shop/config"
    "github.com/Newton-School/gogo"
    "github.com/Newton-School/gogo/core/management"
)
```

Add this `main` function in the same file:

```go
func main() {
    gogo.Script(config.Project(), func(ctx context.Context,
        i *management.Invocation, args []string) error {
        if err := ctx.Err(); err != nil { return err }
        _, err := fmt.Fprintf(i.Stdout, "Registered models: %d\n",
            len(i.Application.Registry.Names("models")))
        return err
    })
}
```

`gogo.Script` loads `.env` and the project's configuration, prepares the app registry, validates selected resource settings, opens `RuntimeResources`, runs app `Ready` hooks, and invokes your callback. Afterwards it runs app shutdown hooks and closes resources. HTTP handlers, migration commands, workers, and schedulers are **not invoked**. Keep your own registration/Ready hooks appropriate for CLI use: Gogo cannot prevent custom hooks or Go `init()` functions from starting work.

The script owns its connections and memory. It does not share the running web server's pools, goroutines, or in-memory state. Both processes can use the same database or Redis URL. No server restart is needed, but the source and dependency versions must match your deployment; Gogo does not automatically verify that match.

## Select resources

`gogo.Script` uses all of `Project.RuntimeResources` by default. Select only the resources required by this script before calling it. The showcase inspection script opens none:

{{snippet examples/showcase/scripts/inspect/main.go runscript-bootstrap}}

In a project with `Ready` hooks that need connections, retain those resources or provide CLI-appropriate app configuration. Required environment variables are determined by the selected resources and your schema, not by `runscript` itself.

## Use the ORM

This complete callback uses the showcase's existing `config.Connections` and `catalog.Product`. It selects PostgreSQL only and exposes the same connection object to the callback:

{{snippet examples/showcase/scripts/catalog-report/main.go runscript-database}}

The showcase's connection factory creates the ORM store when the database resource opens. The script does not run migrations or seed records: prepare the database first. A generated project's `Connections` exposes `Database` rather than `Store`; construct an `orm.Store` with that backend and your registered model schemas, as in the [API tutorial](tutorial-api.md).

<details>
<summary>Complete database script with imports</summary>

{{code examples/showcase/scripts/catalog-report/main.go}}

</details>

## Pass arguments and stdin

Separate script arguments from management flags with `--`. Arguments after it reach the script unchanged, including strings that look like management options:

```sh
./manage runscript scripts/maintenance.go -- --dry-run 42
printf 'input\n' | ./manage runscript scripts/maintenance.go
```

Parse script-specific flags inside your callback, before making changes:

```go
flags := flag.NewFlagSet("maintenance", flag.ContinueOnError)
flags.SetOutput(i.Stderr)
dryRun := flags.Bool("dry-run", true, "preview without making changes")
if err := flags.Parse(args); err != nil { return err }
if *dryRun { return preview(ctx, flags.Args()) }
return apply(ctx, flags.Args())
```

This is a wiring fragment: import `flag` and implement `preview`/`apply` for your own operation. Resource startup has already happened when the callback runs. A `dry-run` flag is your script's contract, not an enforced sandbox. Read input using `i.Stdin`, write output using `i.Stdout`/`i.Stderr`, and pass `ctx` to database/network calls.

## Limits and errors

```sh
./manage runscript --help
./manage runscript scripts/maintenance.go --timeout=10m --max-output=4194304
```

| Option | Default | Behavior |
| --- | --- | --- |
| `--timeout` | `5m` | Positive duration up to `24h`, shared by compilation and execution. Cleanup grace can extend elapsed time. |
| `--max-output` | `1048576` (1 MiB) | Combined compiler/script stdout and stderr budget; accepts 1–67108864 bytes. Exceeding it stops the script and fails. Management error messages are outside this budget. |
| `--` | None | Everything after this separator is a script argument. Put management flags before it. |

Compilation uses the real Go compiler, so generics and normal imports work. It builds a private temporary executable, runs it with the project as its working directory, then removes that temporary directory. Nothing is evaluated in the web process. Ordinary `package main` programs also work without `gogo.Script`, but then they must manage configuration and cleanup themselves.

Builds use `-mod=readonly`, the active workspace/local replacements, the host OS/architecture, and the locally installed toolchain. Automatic toolchain/module downloads, VCS fetching, persistent `go env` defaults, and `GOFLAGS` hooks are disabled. Explicit environment settings for caches/workspaces/CGO are retained; `GOGO_*` settings are excluded from the compiler's environment. Prepare modules and checksums during your trusted build process, not during a production operation. Go's build/module caches remain ordinary Go-managed state.

The child inherits the process environment, and `gogo.Script` reloads its own `config.Project()` and `.env`. Parent-only in-memory settings or connections are not serialized into it. Do not print secrets: compiler diagnostics and script stdout/stderr are streamed, **not automatically redacted**.

| Result | Exit status |
| --- | --- |
| Completed successfully | `0` |
| Invalid arguments or source path | `2` |
| Build/start/output/cleanup failure | `1` |
| Deadline expired | `124` |
| Parent context or management process canceled | `130` |
| Script exited nonzero | Its positive exit code is preserved |

On cancellation, Gogo interrupts the direct script process and allows `GOGO_SHUTDOWN_GRACE` (default `30s`) before forcibly killing it. Windows falls back to terminating the child if interrupt signaling is unavailable. Forced termination cannot run Go defers or guarantee rollback. Scripts that spawn subprocesses must manage them themselves: this is **not process-tree isolation**, a CPU/memory limiter, or a hostile-code sandbox. Use container/job resource limits for production operations.

Return errors from a `gogo.Script` callback; do not call `os.Exit` there, because it bypasses resource cleanup. Ordinary callback errors/panics use the management layer's safe failure output. A failure can occur after writes or external effects have completed. No automatic retries or all-or-nothing transaction are added; check the outcome before retrying.

## Production use

Run only reviewed code with trusted operator access. Possession of this execution capability grants the script the operating-system privileges and credentials of its process. There is no browser/Admin execution endpoint, interactive shell, or live-server attachment.

Use a version-matched operations image or Kubernetes Job containing application source, the compatible Go compiler, cached modules, a writable build cache, and executable temporary storage. Mount only necessary credentials. For read-only work, use read-only database credentials; merely writing a read-only-looking script does not enforce database permissions. Keep normal web images binary-only if desired.

The binary-only showcase web image intentionally cannot compile scripts. From a source-equipped checkout, follow the showcase's native setup and run:

```sh
cd examples/showcase
go run manage.go runscript scripts/inspect/main.go -- demo
go run manage.go runscript scripts/catalog-report/main.go
```

The first prints registration counts without opening PostgreSQL/Redis; the second prints `Products: N` from the already-migrated database. Configure operator access and audit recording through your deployment/job system; this command does not add an identity provider or durable audit store.
