# Opt-in fixture commands

`DumpDataCommand` and `LoadDataCommand` adapt `core/serialization` to an explicitly
registered project. They are not scaffolded or globally discovered. The
application supplies the real backend, exact model profiles, read/write scopes
and current authorization through its resolver; the CLI does not grant access.

```go
resolve := func(ctx context.Context, registry *app.Registry, settings conf.Values,
    alias string) (*serialization.Fixtures, error) {
    // Select only this configured alias from your application's resources.
    // Do not interpret it as a DSN or fall back to another database.
    backend, profiles, err := configuredFixtures(ctx, registry, settings, alias)
    if err != nil {
        return nil, err
    }
    return serialization.New(serialization.Config{
        Backend: backend, Profiles: profiles,
    })
}
dump := management.DumpDataCommand(resolve)
load := management.LoadDataCommand(resolve)
// Only if this project opens those resources to build the selected fixtures:
dump.Resources, load.Resources = []string{"database"}, []string{"database"}
dump.OpenResources, load.OpenResources = true, true
project.Commands = append(project.Commands, dump, load)
```

`configuredFixtures` is application code, not a framework helper. Resource
roles and the project's `ResourceFactory` must match its configuration. Neither
command selects/opens resources by default. Resolvers are trusted cooperative
configuration callbacks; do not perform unrelated effects or create an implicit
backend. A resolved fixture handle must have the exact requested `Alias()`.

```text
manage dumpdata --database=reporting --format=json catalog.Book
manage loaddata --database=reporting --format=jsonl --dry-run catalog.Book
```

Both commands require `--database` and `--format` explicitly. The alias is a
1–128 byte model-style identifier, not a connection string; the format is exactly
`json` or `jsonl`. Supply 1–64 distinct `app.Model` positionals, each component a
1–128 byte identifier. There is no implicit "all", alias default, glob, directory
discovery, file path, `--flush`, natural-key conversion or automatic ID/sequence
repair in this slice. The underlying fixture profiles currently limit raw loads
to complete non-auto keys and supported local values. Its parse, grant, owned
transaction, deferred-constraint and rollback rules remain authoritative.

Normal `management.Call` validates flags and model syntax before opening selected
resources. Help needs no required values or resources. `Command.RequiredFlags`
is a general per-call presence contract: up to 128 distinct names of at most 128
printable ASCII bytes, no leading dash or equals sign. Names must be registered
by `Configure`. Only actual option tokens count, not defaults, programmatic
`FlagSet.Set`, values of another option, or tokens after `--`. Required-name
declarations are captured before configuration callbacks; each `Configure`
owns its parsed values. Fixture options/model names are captured immediately
after successful parsing, before validation/resource/Ready callbacks, so a later
retained `FlagSet.Set` or positional-slice mutation cannot change the operation.
Custom `Configure` wrappers must preserve the registered flag values; removing
the private selection marker makes `Call` refuse execution before the resolver.
As with other Go flag commands, parsing diagnostics may
quote supplied argv; never put credentials or fixture content in flags.

Dump reads through the fixture service and streams **only fixture bytes** to
`Invocation.Stdout`; load reads only `Invocation.Stdin`. Use `management.Options`
to supply those streams for programmatic invocation. The commands do not open
files. A dump error can leave partial output, which must not be consumed as a
complete fixture. It appends no error or summary to that stream and never retries.

A successful load emits one bounded JSON summary followed by a newline:

```json
{"records":3,"committed":true,"dry_run":false}
```

A successful `--dry-run` reports `committed:false,dry_run:true` only after the
service confirms rollback. Load service errors emit no summary. Summary writes
are attempted once; short/invalid counts, writer errors/panics and cancellation
fail the command without retrying or writing an error body to stdout.

Use `errors.As(err, &fixtureErr)` with `*management.FixtureCommandError` to inspect
the actual `DumpResult` or `LoadResult` when command completion fails. A load may
already have committed even if the summary is missing, partial or complete.
`Load.Committed` stays true after later summary/cancellation failure. Existing
`serialization.ErrOutcomeUnknown` and `ErrCommittedCallback` remain available
through `errors.Is`. A resolver returning one of those sentinels cannot create
an operation receipt: inspect the receipt, not a foreign cause, for confirmed
completion. Zero results never promise that retry is safe. Reconcile unknown
database outcomes and output before deciding what to do next.

Command errors have fixed safe text and project causes to a bounded set of safe
serialization, context and IO sentinels. Raw resolver/writer/provider error
details are not retained. Contexts, IO implementations and resolver callbacks
are trusted cooperative code, not a sandbox. Invocation handles, flags, model
selection and the returned fixture handle are captured before subsequent
callbacks; inherited context objects and provider internals remain their owners'
responsibility. Whole concurrent unsynchronized mutation of those owners is not
supported. A `Call` owns one fixture operation: custom wrappers cannot start a
second or reentrant fixture operation and overwrite its receipt. Standalone
configured runners do not share that `Call` holder. Cleanup failure (including a
wrapper panic after its fixture runner returns) is projected through the captured
receipt, with closers still called once. Already written bytes or committed rows
cannot be rolled back by command exit.
