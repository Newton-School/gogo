# OpenAPI management command

`OpenAPICommand` adds opt-in `go run manage.go openapi` emission for the [bound read API](../api/openapi.md). It uses `api.OpenAPI`; it does not discover whole-site routes, infer missing schemas, or install authentication.

Register the command in the application's existing `management.Project.Commands`. The factory should be the same selected API-router factory used by the web application, before surrounding middleware:

```go
import (
    "context"

    "github.com/Newton-School/gogo/core/api"
    "github.com/Newton-School/gogo/core/app"
    "github.com/Newton-School/gogo/core/conf"
    "github.com/Newton-School/gogo/core/management"
    "github.com/Newton-School/gogo/core/urls"
)

func withOpenAPI(
    project management.Project,
    buildAPI func(context.Context, *app.Registry, conf.Values) (*urls.Router, error),
) management.Project {
    command := management.OpenAPICommand(buildAPI, api.OpenAPIOptions{
        Title: "Catalog API", Version: "1.0.0",
    })
    project.Commands = append(project.Commands, command)
    return project
}
```

The configured factory returns a `urls.New` router containing `Resource.ReadOpenAPIRoutes` and supported literal includes. Public serializer fields and explicit security declarations remain mandatory. An opaque middleware-wrapped handler is not a route descriptor. The application still needs real authentication, authorization, scope and an opened backend to serve its HTTP reads.

## Invocation and resources

The command has no positional arguments or custom flags. Normal `management.Call`/`Run` handling supports `--help` and rejects unknown flags or extra arguments before resource startup. Title/version are explicit Go configuration, not inferred from settings, a request host, or model names.

No resources are selected or opened by default. If the shared factory requires a configured connection, opt in through the existing command fields before registration:

```go
command.Resources = []string{"database"}
command.OpenResources = true
```

The project supplies its ordinary `ResourceFactory`; only those declared resources are requested. The normal management lifecycle closes them after success or failure. A factory is a trusted, cooperative read-only callback: honor its context, avoid queries and samples, and do not mutate the registry. The command never copies the registry's mutex or invokes the web handler. Application preparation and registry hooks remain part of normal management startup; this is not a callback sandbox or a new lifecycle.

## Output and failures

The complete bounded document is generated before a single `Invocation.Stdout.Write`. Its bytes exactly match `api.OpenAPI`, with no added newline. The command captures its writer, settings and registry pointer before callbacks, and captures the resolved router handle before checking context again. Unsupported routes, missing schemas, invalid options, provider errors, cancellation and panics return safe nonzero command failures; application causes remain available to programmatic callers through `errors.Is`/`errors.As`, not printed as provider details by this command.

Short or invalid write counts and writer errors fail without retrying or appending error JSON. Output cannot be rolled back: a write error, post-write cancellation, or later resource-cleanup failure may leave partial **or complete** document bytes despite a nonzero result. Consumers must check command success before publishing an artifact. The command creates no file; shell redirection is the caller's responsibility and can truncate its destination before command execution.

This completes only opt-in read-schema command emission. Mutation/input schemas, typed clients, automatic project-scaffold registration and a browsable documentation UI are separate work.
