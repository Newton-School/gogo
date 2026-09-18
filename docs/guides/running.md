# Run, reload, and build your application

Gogo gives you Django-like management commands, but your application is compiled Go. **A Go binary runs the HTTP server.** There is no Python interpreter, Gunicorn, or separately installed Gogo runtime in the serving path.

## Run the development server

From the client directory containing `manage.go` and the configured `.env`:

```sh
go run manage.go check
go run manage.go runserver
```

Go compiles the entrypoint and starts it. Open [localhost:8000](http://localhost:8000/). The generated root route returns `{"framework":"gogo"}`. Stop it with Ctrl+C.

To change the listener for this invocation:

```sh
go run manage.go runserver --addr 127.0.0.1:8001
```

## Rebuild when source changes

On Linux or macOS:

```sh
go run manage.go runserver --reload
```

Save a Go file and the development supervisor rebuilds and restarts the child. This is not Python-style module reloading. Build/preflight failure must not be mistaken for a successful update; check the terminal and fix the source. Reload is not available in production mode.

For an additional embedded-asset directory, supply a project-relative watch path:

```sh
go run manage.go runserver --reload --watch templates
```

Watching a directory does not serve it publicly. Register static handlers or template loaders separately.

## Compile once and run the binary

```sh
go build -trimpath -o bin/manage .
./bin/manage help
./bin/manage serve
```

Run these from the client project root so relative `.env`/asset paths resolve as configured. After editing source, rebuild the binary. On Windows, use `go build -o bin/manage.exe .` and the resulting `.exe`; development reload is Linux/macOS-only.

`go build` does not need database passwords and does not embed `.env`. Starting a resource-dependent command still validates its runtime settings. On a deployed host, supply environment variables and any non-embedded assets alongside the executable.

## One image, separate processes

| Process | Command | Finishes when |
| --- | --- | --- |
| HTTP server | `./bin/manage serve` | Shutdown or server failure |
| Migration job | `./bin/manage migrate` | Migration operation completes or fails |
| Worker, after Async registration | `./bin/manage worker --queues default` | Shutdown or worker failure |
| Scheduler, after Beat registration | `./bin/manage beat` | Shutdown or scheduler failure |

The queue name must be allowed by your worker factory. Put these commands in different pods/containers to scale them independently. Do not run migrations as an uncoordinated startup step in every replica. `GOGO_ENV` chooses environment policy; it is **not** a process-role switch. There is no built-in `GOGO_MODE` registry in this release.

## Framework checkout versus client project

`make build` in the Gogo repository compiles framework workspace packages. It does not create your application server. Build the project containing **your** `manage.go` instead. Clients inside the contributor checkout need `GOWORK=off` to avoid the framework workspace; creating them outside it is simpler.

Next: [build a database-backed API](tutorial-api.md) or [run your app with Docker](docker.md).
