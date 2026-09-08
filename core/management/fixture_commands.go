package management

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/serialization"
)

// FixtureCommandError retains the actual serialization operation's receipt if
// command completion fails. A zero receipt is not proof that a write did not
// occur. In particular, inspect ErrOutcomeUnknown rather than automatically
// retrying. Cause contains only projected safe serialization/context/IO
// sentinels, never raw resolver or provider details. Resolver errors cannot
// manufacture a committed receipt, even if they wrap a completion sentinel.
type FixtureCommandError struct {
	Command string
	Dump    serialization.DumpResult
	Load    serialization.LoadResult
	Cause   error
}

func (e *FixtureCommandError) Error() string {
	if e != nil && e.Load.Committed {
		return "Fixture load committed; command completion failed"
	}
	if e != nil && e.Load.DryRun {
		return "Fixture dry run rolled back; command completion failed"
	}
	if e != nil && e.Dump.Complete {
		return "Fixture export completed; command completion failed"
	}
	return "Fixture command failed; reconcile any output and database outcome before retrying"
}
func (e *FixtureCommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// DumpDataCommand returns an opt-in dumpdata command. Required --database and
// --format flags select a configured alias and json/jsonl; one or more explicit
// app.Model arguments select registered profiles. Resolve is trusted,
// cooperative application configuration, not automatic backend discovery.
// No resources are selected/opened by default: set Resources/OpenResources on
// the returned command when its resolver needs the application's resources.
// Dump writes only fixture bytes to Invocation.Stdout. A failure can leave a
// partial document; it never appends a summary/error or retries the stream.
func DumpDataCommand(resolve func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error)) Command {
	return fixtureCommand(false, resolve)
}

// LoadDataCommand returns an opt-in loaddata command using the same required
// selection as DumpDataCommand. It reads Invocation.Stdin, and --dry-run checks
// then rolls back through serialization.Load. Only a successful load/dry run
// writes one JSON receipt to Stdout. Later output/cancellation failure retains
// the observed LoadResult in FixtureCommandError; confirmed data cannot be
// rolled back by output failure. No files, flush or automatic retries are used.
func LoadDataCommand(resolve func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error)) Command {
	return fixtureCommand(true, resolve)
}

type fixtureCommandOptions struct {
	database string
	format   serialization.Format
	dryRun   bool
}

type fixtureCommandSelection struct {
	options fixtureCommandOptions
	frozen  *fixtureCommandSnapshot
}
type fixtureCommandSnapshot struct {
	options fixtureCommandOptions
	models  []string
}
type fixtureCommandFlag struct {
	flag.Value
	selection *fixtureCommandSelection
}

// flag.PrintDefaults asks a reflection-created zero Value for its String.
func (value *fixtureCommandFlag) String() string {
	if value == nil || value.Value == nil {
		return ""
	}
	return value.Value.String()
}

// The marker is private and belongs to this Configure/FlagSet only. Capture
// after successful parsing, before Validate/resource callbacks may retain and
// mutate FlagSet values. Other commands have no marker and are unchanged.
func captureFixtureCommandSelection(flags *flag.FlagSet, args []string) error {
	var selected *fixtureCommandSelection
	conflict := false
	flags.VisitAll(func(f *flag.Flag) {
		if value, ok := f.Value.(*fixtureCommandFlag); ok {
			if selected != nil && selected != value.selection {
				conflict = true
			}
			selected = value.selection
		}
	})
	if selected == nil {
		return nil
	}
	if conflict || selected.frozen != nil || validateFixtureModels(args) != nil || !fixtureCommandName(selected.options.database) || selected.options.format != serialization.JSON && selected.options.format != serialization.JSONL {
		return serialization.ErrConfiguration
	}
	selected.frozen = &fixtureCommandSnapshot{options: selected.options, models: slices.Clone(args)}
	return nil
}

func fixtureCommand(load bool, resolve func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error)) Command {
	name, help := "dumpdata", "Export explicitly selected fixture profiles to stdout"
	if load {
		name, help = "loaddata", "Load explicitly selected fixture profiles from stdin in one owned transaction"
	}
	return Command{Name: name, Help: help, RequiredFlags: []string{"database", "format"}, Validate: validateFixtureModels, Configure: func(flags *flag.FlagSet) Runner {
		selection := &fixtureCommandSelection{}
		options := &selection.options
		flags.Func("database", "required configured database alias (not a connection string)", func(value string) error {
			if !fixtureCommandName(value) {
				return errors.New("a valid configured database alias is required")
			}
			options.database = value
			return nil
		})
		flags.Func("format", "required fixture format: json or jsonl", func(value string) error {
			format := serialization.Format(value)
			if format != serialization.JSON && format != serialization.JSONL {
				return errors.New("fixture format must be json or jsonl")
			}
			options.format = format
			return nil
		})
		definition := flags.Lookup("database")
		definition.Value = &fixtureCommandFlag{Value: definition.Value, selection: selection}
		if load {
			flags.BoolVar(&options.dryRun, "dry-run", false, "validate the complete load and roll back without committing")
		}
		return func(ctx context.Context, invocation *Invocation, args []string) error {
			// Each Configure owns its flag values. Freeze the selected values
			// before callbacks can call FlagSet.Set on that invocation.
			selected := selection.options
			names := args
			if selection.frozen != nil {
				selected = selection.frozen.options
				names = selection.frozen.models
			}
			return runFixtureCommand(ctx, invocation, names, load, selected, selection.frozen != nil, resolve)
		}
	}}
}

func fixtureCommandName(value string) bool {
	return len(value) > 0 && len(value) <= 128 && models.ValidIdentifier(value)
}

func validateFixtureModels(args []string) error {
	if len(args) == 0 || len(args) > 64 {
		return errors.New("between one and 64 explicit fixture models are required")
	}
	seen := make(map[string]bool, len(args))
	for _, value := range args {
		if len(value) > 257 || seen[value] {
			return errors.New("invalid or duplicate fixture model selection")
		}
		appLabel, model, ok := strings.Cut(value, ".")
		if !ok || !fixtureCommandName(appLabel) || !fixtureCommandName(model) {
			return errors.New("fixture models must use explicit app.Model names")
		}
		seen[value] = true
	}
	return nil
}

func runFixtureCommand(ctx context.Context, invocation *Invocation, args []string, load bool, options fixtureCommandOptions, prepared bool, resolve func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error)) (err error) {
	receipt := FixtureCommandError{Command: "dumpdata"}
	if load {
		receipt.Command = "loaddata"
	}
	failure := func(cause error) error { return fixtureCommandFailure(receipt, cause) }
	var completion *fixtureCommandCompletion
	defer func() {
		if recover() != nil {
			err = failure(errors.Join(serialization.ErrUnavailable, fixtureCommandContextError(ctx)))
		}
		if completion != nil {
			if err != nil {
				// The command's projected cause is already safe. Retain it
				// separately so a later hostile cleanup graph cannot consume
				// the traversal budget before this known outcome is visited.
				receipt.Cause = fixtureCommandCause(err)
			}
			completion.record(receipt)
		}
	}()
	if validateFixtureModels(args) != nil || !fixtureCommandName(options.database) || options.format != serialization.JSON && options.format != serialization.JSONL {
		return &CommandError{Code: 2, Message: "Invalid fixture command arguments"}
	}
	if invocation == nil || invocation.Application == nil || invocation.Application.Registry == nil || resolve == nil || fixtureCommandNil(invocation.Stdout) || load && fixtureCommandNil(invocation.Stdin) {
		return failure(serialization.ErrConfiguration)
	}
	// Never copy the Registry mutex. IO/configuration handles and bounded model
	// names are fixed before the first context or resolver callback.
	reader, writer := invocation.Stdin, invocation.Stdout
	registry, settings := invocation.Application.Registry, invocation.Settings
	names := slices.Clone(args)
	if !fixtureCommandNil(ctx) {
		completion, _ = ctx.Value(fixtureCommandCompletionKey{}).(*fixtureCommandCompletion)
	}
	if completion != nil && !completion.claim() {
		// A wrapper must not replace a prior confirmed receipt with the
		// result of another/reentrant operation in the same Call.
		completion = nil
		return failure(serialization.ErrConfiguration)
	}
	if completion != nil && !prepared {
		// A custom Configure wrapper removed the private flag marker. Call
		// must not fall back to options that resource callbacks could alter.
		return failure(serialization.ErrConfiguration)
	}
	if err := fixtureCommandContextError(ctx); err != nil {
		return failure(err)
	}
	fixtures, resolveErr := resolve(ctx, registry, settings, options.database)
	if fixtures != nil {
		captured := *fixtures // immutable private-state handle, not a mutex copy
		fixtures = &captured
	}
	if err := errors.Join(resolveErr, fixtureCommandContextError(ctx)); err != nil {
		return failure(err)
	}
	if fixtures == nil || fixtures.Alias() != options.database {
		return failure(serialization.ErrConfiguration)
	}
	if !load {
		output := &fixtureCommandWriter{writer: writer}
		result, dumpErr := fixtures.Dump(ctx, output, serialization.DumpOptions{Format: options.format, Models: names})
		receipt.Dump = result // capture before another arbitrary context callback
		if err := errors.Join(dumpErr, output.cause, fixtureCommandContextError(ctx)); err != nil {
			return failure(err)
		}
		return nil
	}
	result, loadErr := fixtures.Load(ctx, reader, serialization.LoadOptions{Format: options.format, Models: names, DryRun: options.dryRun})
	receipt.Load = result // the actual operation, never a nested foreign error
	if err := errors.Join(loadErr, fixtureCommandContextError(ctx)); err != nil {
		return failure(err)
	}
	data, encodeErr := json.Marshal(struct {
		Records   int  `json:"records"`
		Committed bool `json:"committed"`
		DryRun    bool `json:"dry_run"`
	}{result.Records, result.Committed, result.DryRun})
	if encodeErr != nil {
		return failure(encodeErr)
	}
	output := &fixtureCommandWriter{writer: writer}
	_, writeErr := output.Write(append(data, '\n'))
	if err := errors.Join(writeErr, fixtureCommandContextError(ctx)); err != nil {
		return failure(err)
	}
	return nil
}

func fixtureCommandFailure(receipt FixtureCommandError, cause error) error {
	receipt.Cause = errors.Join(receipt.Cause, fixtureCommandCause(cause))
	return &CommandError{Code: 1, Message: receipt.Error(), Cause: &receipt}
}

// Only the fixture runner can install a receipt; the private per-Call holder is
// not carried on the public Invocation and is never shared between Calls.
type fixtureCommandCompletionKey struct{}
type fixtureCommandCompletion struct {
	mu           sync.Mutex
	receipt      FixtureCommandError
	set, claimed bool
}

func (c *fixtureCommandCompletion) claim() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claimed {
		return false
	}
	c.claimed = true
	return true
}

func (c *fixtureCommandCompletion) record(receipt FixtureCommandError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.receipt, c.set = receipt, true
}
func (c *fixtureCommandCompletion) projector() func(error) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	receipt, set := c.receipt, c.set
	c.mu.Unlock()
	if !set {
		return nil
	}
	return func(cause error) error { return fixtureCommandFailure(receipt, cause) }
}

// The export serializer may redact provider errors. Capture output failures so
// the command can project safe IO identities without retaining raw diagnostics.
type fixtureCommandWriter struct {
	writer io.Writer
	cause  error
}

func (w *fixtureCommandWriter) Write(data []byte) (n int, err error) {
	defer func() {
		if recover() != nil {
			err = serialization.ErrUnavailable
		}
		if n != len(data) {
			err = errors.Join(err, io.ErrShortWrite)
		}
		if err != nil {
			w.cause = errors.Join(w.cause, err)
		}
	}()
	return w.writer.Write(data)
}

func fixtureCommandNil(value any) bool {
	if value == nil {
		return true
	}
	r := reflect.ValueOf(value)
	switch r.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return r.IsNil()
	}
	return false
}

func fixtureCommandContextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = serialization.ErrUnavailable
		}
	}()
	if fixtureCommandNil(ctx) {
		return serialization.ErrUnavailable
	}
	switch err := ctx.Err(); err {
	case nil, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return serialization.ErrUnavailable
	}
}

// Do not call Error or custom Is/As methods, and do not retain unknown errors.
// Unwrap is trusted cooperative code, but its graph and panic are bounded here.
func fixtureCommandCause(cause error) (out error) {
	defer func() {
		if recover() != nil {
			out = errors.Join(out, serialization.ErrUnavailable)
		}
	}()
	known := []error{serialization.ErrConfiguration, serialization.ErrInvalid, serialization.ErrForbidden, serialization.ErrUnavailable, serialization.ErrLimit, serialization.ErrTransaction, serialization.ErrOutcomeUnknown, serialization.ErrCommittedCallback, context.Canceled, context.DeadlineExceeded, io.ErrShortWrite, io.ErrClosedPipe, io.ErrUnexpectedEOF, io.EOF}
	pending := []error{cause}
	seen := make([]bool, len(known))
	for visits := 0; len(pending) > 0; visits++ {
		if visits >= 64 {
			return errors.Join(out, serialization.ErrUnavailable)
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		matched := false
		for index, safe := range known {
			if current == safe { // every known dynamic type is comparable
				matched = true
				if !seen[index] {
					out = errors.Join(out, safe)
					seen[index] = true
				}
				break
			}
		}
		if matched {
			continue
		}
		switch value := current.(type) {
		case interface{ Unwrap() []error }:
			children := value.Unwrap()
			if len(children) > 64-len(pending)-visits {
				return errors.Join(out, serialization.ErrUnavailable)
			}
			pending = append(pending, children...)
		case interface{ Unwrap() error }:
			pending = append(pending, value.Unwrap())
		default:
			if !seen[3] {
				out = errors.Join(out, serialization.ErrUnavailable)
				seen[3] = true
			}
		}
	}
	if out == nil {
		return serialization.ErrUnavailable
	}
	return out
}
