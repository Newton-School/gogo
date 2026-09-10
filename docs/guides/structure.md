# Projects and applications

A **project** owns deployment configuration, connections, root routes and management commands. An **app** groups one area's models, views, forms, serializers, tasks and migrations. Apps are Go packages, not dynamically imported Python modules.

## Project layout

The generator creates the app skeleton below. Add `admin.go` and `tasks.go` yourself when those optional modules are needed; they are conventional application files, not generated integrations.

```text
storefront/
├── manage.go                 central command entrypoint
├── config/
│   ├── settings.go           typed settings and Project factory
│   ├── apps.go               explicit InstalledApps
│   ├── connections.go        PostgreSQL ownership and cleanup
│   └── urls.go               root URL tree
├── apps/catalog/
│   ├── app.go                app registration
│   ├── models.go             Go structs and Schema methods
│   ├── views.go              HTTP behavior
│   ├── urls.go               app routes
│   ├── forms.go              form definitions
│   ├── serializers.go        API representations
│   ├── services.go           transactional business operations
│   ├── admin.go              add for optional Admin declarations
│   ├── tasks.go              add for optional Async definitions
│   ├── migrations/           migration source and registry
│   ├── templates/            app templates
│   └── static/               app assets
├── templates/                project templates
├── static/                   project assets
└── tests/integration/        application integration tests
```

`generate` adds `zz_gogo.gen.go` for model registrations and typed query references. Commit generated descriptors and migration source; regenerate them rather than editing them directly. Empty files created by `startapp` are organizational starting points, not auto-installed services.

## Project wiring

`management.Project` combines `Schema`, `Apps`, `Commands`, `Handler`, `RuntimeResources` and `ResourceFactory`. `gogo.Main(config.Project())` dispatches the selected command. The resource factory receives only the resources that command requests.

Use `app.Config.Requires` to declare app dependencies. `Register` contributes descriptors to the registry. `Ready` performs startup work after resources open. `Shutdown` participates in cleanup. Keep registration free of external I/O; do not launch a worker as an accidental side effect of importing an app.

## Startup and shutdown

```text
Project configuration
    ↓
Order apps by dependency → reject missing dependencies/cycles
    ↓
Register descriptors and freeze the registry
    ↓
Select command → validate flags and required settings
    ↓
Open selected resources → run app Ready hooks
    ↓
Run server, worker, migration or custom command
    ↓
Stop admission and drain owned work
    ↓
Run shutdown hooks and close resources
```

Failed startup must not be treated as readiness. Application shutdown owns resources, but the command/server owner must drain work before closing the pools that work uses.

## Separate deployments, shared code

Run the same compiled application with different commands for web, worker and scheduler processes. Gogo does not currently provide a built-in `GOGO_MODES` registry. A custom router or a specialized background process belongs in explicit project wiring; see [Deployment](deployment.md).
