# Development reload

Run the generated project's usual entry point:

```sh
go run manage.go runserver --reload
go run manage.go runserver --reload --addr=127.0.0.1:8000 --watch=assets
go run manage.go generate --check
```

`runserver` without `--reload` starts one application. `serve` remains the plain
server command and does not accept reload/watch options. The initial `go run`
must compile before the supervisor can start; subsequent build failures retain
the running child. Reload currently supports Linux and macOS only.

## Replacement sequence

1. Watch content changes, coalesce edits and check generated descriptors without
   writing source. Run `generate` explicitly to repair stale descriptors.
2. Build `manage.go` with `go build -trimpath` into a private temporary directory.
   Compiler diagnostics go to the invocation's terminal, not an HTTP error page.
3. Run that exact candidate's existing `check` and `diffsettings` commands. Its
   fresh schema, defaults and code-defined environment own validation. Capture
   configuration privately, validate a bounded lifecycle/output-root projection,
   and reject production candidates. Captured settings are never printed.
4. If output-root exclusions changed, retain the child, establish the new watch
   baseline and rebuild. Hashes made with different selections are not compared.
5. Only a valid, current candidate may drain the old child. Observe its exit
   before starting a replacement. Recheck source and cancellation after drain.
6. Start the new binary with plain `runserver --addr ...`, never recursively with
   `--reload`. Each child builds fresh registries and owns its runtime resources.

Invalid source, stale descriptors, failed builds/checks and invalid replacement
configuration keep the healthy child and wait for another edit. A child start
failure or failed shutdown ends supervision; runtime startup is not guaranteed
by compilation/preflight. There is no automatic migration, fallback replay or
production hot-code replacement. A successful process start is not readiness:
the listening diagnostic is emitted only after the real listener is bound.

The supervisor/preflight commands do not call the framework's resource factory,
`Ready` hooks or HTTP handler. Normal app registration, freeze hooks and explicitly
registered read-only system checks still run. Those callbacks and the compiled
project are trusted application code, not a sandbox.

## Source selection

Default selection includes Go files, Go module/workspace files, the root `.env`
and files below template directories. Repeat `--watch` for additional embedded
assets or custom template directories; it watches their contents but serves
nothing. At most 32 real, canonical project-relative directories may be added.

Ignore `.git`, other hidden entries except the root `.env`, editor temporary
files, dependency/cache directories, conventional root build/media/upload
directories and the resolved configured storage/static-output roots. An app
named `media` or `build` is still watched. Discovered symlinks are not followed;
explicit watch-directory symlinks and output roots containing the project are
rejected. Private uploads are not development static assets.

Poll and debounce intervals are each 200 ms. A snapshot is bounded to 32,768
directory entries, depth 32, 16 MiB per selected file and 128 MiB of selected
content. `.env` and private preflight output are each limited to 1 MiB. Limits
refuse work rather than silently truncate the watch selection. Source reads and
terminal writers must cooperate; a fingerprint is not an atomic filesystem
snapshot or protection against hostile same-privilege writers.

The listen address and process environment are captured when supervision starts.
Restart to change them. `.env`, application settings/schema, code-defined
environment values and watched project files are re-evaluated for candidates.
External replaced Go modules and other trees are not implicitly watched.

## Shutdown and assets

Each build/preflight generation has a two-minute deadline. Only directly owned
process handles are signaled: the Go tool receives Interrupt; a generated server
receives SIGTERM. HTTP drain and subsequent app cleanup each have the configured
shutdown grace, so the supervisor allows twice that grace plus one second.
Reload requires a positive grace no greater than one minute.

After the shutdown deadline, the supervisor may force-stop its directly owned
child and must observe its actual OS exit before replacement. Forced replacement
is explicitly reported. Nonzero graceful cleanup or output-copy failures do not
become successful shutdowns. Application-spawned descendants remain the
application's responsibility; there is no process-group discovery/signaling.
Applications must retain explicit closers for hijacked WebSockets and other
work not owned by the ordinary HTTP drain.

For public development assets, explicitly mount an existing
`static.Collector.DevHandler(settings.Bool("GOGO_DEBUG"))` from the application's
handler configuration. False refuses construction. Reload does not discover,
mount or publish assets, and does not expose private media.

Programmatic callers may use `RunServer` with a prepared `Invocation` and
`RunServerOptions`. `management.Call` owns application cleanup; direct callers
must close their prepared application after `RunServer` returns. Stable server
CLI failures never promote callback-supplied messages or exit codes.
