package management

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/serialization"
)

// A deliberately tiny transaction provider exercises the public serializer,
// not a command-only mock result. Native command tests cover real PostgreSQL.
type commandFixtureDialect struct{}

func (commandFixtureDialect) Name() string { return "fixture_command" }
func (commandFixtureDialect) QuoteIdentifier(s string) (string, error) {
	if !models.ValidIdentifier(s) {
		return "", serialization.ErrInvalid
	}
	return `"` + s + `"`, nil
}
func (commandFixtureDialect) Placeholder(n int) string               { return "$" + strconv.Itoa(n) }
func (commandFixtureDialect) FieldType(models.Field) (string, error) { return "TEXT", nil }

type commandFixtureBackend struct {
	db.Backend
	rows                                [][]any
	begins, inserts, commits, rollbacks int
	commitErr                           error
	commitHook                          func()
}

func (*commandFixtureBackend) Alias() string       { return "fixtures" }
func (*commandFixtureBackend) Dialect() db.Dialect { return commandFixtureDialect{} }
func (*commandFixtureBackend) Capabilities() db.Capabilities {
	return db.Capabilities{"transactions": true}
}
func (b *commandFixtureBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.begins++
	return &commandFixtureTx{b: b, rows: append([][]any(nil), b.rows...)}, nil
}

type commandFixtureTx struct {
	db.Transaction
	b    *commandFixtureBackend
	rows [][]any
}

func (tx *commandFixtureTx) Query(_ context.Context, sql string, args ...any) (db.Rows, error) {
	if strings.HasPrefix(sql, "INSERT") {
		tx.b.inserts++
		row := append([]any(nil), args...)
		tx.rows = append(tx.rows, row)
		return &commandFixtureRows{rows: [][]any{row}}, nil
	}
	rows := tx.rows
	if strings.Contains(sql, `"id" =`) {
		rows = nil
		for _, row := range tx.rows {
			if reflect.DeepEqual(row[0], args[0]) {
				rows = append(rows, row)
			}
		}
	}
	return &commandFixtureRows{rows: rows}, nil
}
func (tx *commandFixtureTx) CheckConstraints(context.Context) error { return nil }
func (tx *commandFixtureTx) Commit() error {
	tx.b.commits++
	if tx.b.commitErr == nil {
		tx.b.rows = tx.rows
	}
	if tx.b.commitHook != nil {
		tx.b.commitHook()
	}
	return tx.b.commitErr
}
func (tx *commandFixtureTx) Rollback() error { tx.b.rollbacks++; return nil }

type commandFixtureRows struct {
	rows  [][]any
	index int
}

func (*commandFixtureRows) Columns() ([]string, error) { return nil, nil }
func (r *commandFixtureRows) Next() bool {
	if r.index == len(r.rows) {
		return false
	}
	r.index++
	return true
}
func (r *commandFixtureRows) Scan(dest ...any) error {
	for i, d := range dest {
		*d.(*any) = r.rows[r.index-1][i]
	}
	return nil
}
func (*commandFixtureRows) Close() error { return nil }
func (*commandFixtureRows) Err() error   { return nil }

func fixtureCommandProvider(t *testing.T, b *commandFixtureBackend, grant func(context.Context, serialization.Action, serialization.Record) error) *serialization.Fixtures {
	t.Helper()
	if grant == nil {
		grant = func(context.Context, serialization.Action, serialization.Record) error { return nil }
	}
	f, err := serialization.New(serialization.Config{Backend: b, Profiles: []serialization.ModelProfile{{Schema: models.Schema{AppLabel: "example", Name: "Note", Fields: []models.Field{models.BigIntegerField("id", models.Primary), models.TextField("title")}}, Fields: []string{"id", "title"}, Import: true, Scope: func(context.Context, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil }, Authorize: grant}}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

const fixtureCommandInput = `[{"version":1,"model":"example.Note","pk":{"id":{"value":1}},"fields":{"title":{"value":"safe title"}}}]`

func fixtureCommandInvocation(t *testing.T, stdout io.Writer) *Invocation {
	t.Helper()
	a, err := app.Prepare(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := (conf.Schema{{Name: "GOGO_FIXTURE_MARKER", Default: "original"}}).Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Invocation{Application: a, Settings: settings, Stdin: strings.NewReader(fixtureCommandInput), Stdout: stdout}
}
func configureFixtureCommand(t *testing.T, command Command, flags ...string) (Runner, *flag.FlagSet) {
	t.Helper()
	set := flag.NewFlagSet(command.Name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	run := command.Configure(set)
	if err := set.Parse(flags); err != nil {
		t.Fatal(err)
	}
	return run, set
}
func commandFixtureResolver(f *serialization.Fixtures) func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
	return func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
		return f, nil
	}
}
func fixtureCommandError(t *testing.T, err error) *FixtureCommandError {
	t.Helper()
	var command *CommandError
	var result *FixtureCommandError
	if !errors.As(err, &command) || command.Code != 1 || !errors.As(err, &result) {
		t.Fatalf("wrong safe command failure: %v", err)
	}
	return result
}

func TestFixtureCommandsRequireExplicitSelectionBeforeResources(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, load := range []bool{false, true} {
		for _, args := range [][]string{nil, {"example.Note", "--format=json"}, {"example.Note", "--database=fixtures"}, {"example.Note", "--database=", "--format=json"}, {"example.Note", "--database=invalid/alias", "--format=json"}, {"example.Note", "--database=fixtures", "--format="}, {"example.Note", "--database=fixtures", "--format=yaml"}, {"--database=fixtures", "--format=json"}, {"example.Note", "example.Note", "--database=fixtures", "--format=json"}, {"*", "--database=fixtures", "--format=json"}, {"example.Note", "--database=fixtures", "--format=json", "--unknown"}, {"--help"}} {
			calls := 0
			resolve := func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
				calls++
				return nil, nil
			}
			command := DumpDataCommand(resolve)
			if load {
				command = LoadDataCommand(resolve)
			}
			if command.OpenResources || len(command.Resources) != 0 {
				t.Fatal("implicit resources")
			}
			command.OpenResources = true
			project := Project{Root: t.TempDir(), Commands: []Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { calls++; return nil, nil }}
			err := Call(context.Background(), project, append([]string{command.Name}, args...), Options{Stdin: strings.NewReader("secret fixture"), Stdout: io.Discard, Stderr: io.Discard})
			help := len(args) == 1 && args[0] == "--help"
			if calls != 0 || help && err != nil || !help && err == nil {
				t.Fatalf("%s %v: %v callbacks=%d", command.Name, args, err, calls)
			}
		}
	}
}

func TestFixtureCommandsPublicSerializerRoundTripAndDryRun(t *testing.T) {
	for _, format := range []serialization.Format{serialization.JSON, serialization.JSONL} {
		b := &commandFixtureBackend{rows: [][]any{{int64(1), "safe title"}}}
		f := fixtureCommandProvider(t, b, nil)
		var dump, expected bytes.Buffer
		if _, err := f.Dump(context.Background(), &expected, serialization.DumpOptions{Format: format, Models: []string{"example.Note"}}); err != nil {
			t.Fatal(err)
		}
		run, _ := configureFixtureCommand(t, DumpDataCommand(commandFixtureResolver(f)), "--database=fixtures", "--format="+string(format))
		if err := run(context.Background(), fixtureCommandInvocation(t, &dump), []string{"example.Note"}); err != nil || dump.String() != expected.String() {
			t.Fatal(err, dump.String())
		}
		for _, dry := range []bool{true, false} {
			target := &commandFixtureBackend{}
			g := fixtureCommandProvider(t, target, nil)
			var summary bytes.Buffer
			load, _ := configureFixtureCommand(t, LoadDataCommand(commandFixtureResolver(g)), "--database=fixtures", "--format="+string(format), "--dry-run="+strconv.FormatBool(dry))
			inv := fixtureCommandInvocation(t, &summary)
			inv.Stdin = bytes.NewReader(dump.Bytes())
			if err := load(context.Background(), inv, []string{"example.Note"}); err != nil {
				t.Fatal(err)
			}
			want := "{\"records\":1,\"committed\":" + strconv.FormatBool(!dry) + ",\"dry_run\":" + strconv.FormatBool(dry) + "}\n"
			if summary.String() != want || target.inserts != 1 || target.commits != boolInt(!dry) || len(target.rows) != boolInt(!dry) {
				t.Fatal(summary.String(), target)
			}
		}
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type fixtureCommandTestWriter struct {
	bytes.Buffer
	calls int
	write func([]byte) (int, error)
}

func (w *fixtureCommandTestWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.write != nil {
		return w.write(p)
	}
	return w.Buffer.Write(p)
}

type fixturePrivateError struct{}

func (fixturePrivateError) Error() string { panic("Error method must not run") }

func TestFixtureLoadSummaryFailureRetainsActualReceiptWithoutPrivateCause(t *testing.T) {
	for _, mode := range []string{"short", "negative", "large", "error", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			b := &commandFixtureBackend{}
			f := fixtureCommandProvider(t, b, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &fixtureCommandTestWriter{write: func(data []byte) (int, error) {
				switch mode {
				case "short":
					return 1, nil
				case "negative":
					return -1, nil
				case "large":
					return len(data) + 1, nil
				case "error":
					return 0, fixturePrivateError{}
				case "panic":
					panic(fixturePrivateError{})
				case "cancel":
					cancel()
					return len(data), nil
				}
				return len(data), nil
			}}
			run, _ := configureFixtureCommand(t, LoadDataCommand(commandFixtureResolver(f)), "--database=fixtures", "--format=json")
			err := run(ctx, fixtureCommandInvocation(t, writer), []string{"example.Note"})
			receipt := fixtureCommandError(t, err)
			if !receipt.Load.Committed || receipt.Load.Records != 1 || receipt.Load.DryRun || b.commits != 1 || writer.calls != 1 {
				t.Fatal(receipt.Load, b.commits, writer.calls)
			}
			var private fixturePrivateError
			if errors.As(err, &private) {
				t.Fatal("raw writer cause retained")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost post-commit cancellation")
			}
		})
	}
}

func TestFixtureLoadUnknownAndCommittedCallbackRemainDistinct(t *testing.T) {
	for _, kind := range []string{"unknown", "callback", "dry_output"} {
		t.Run(kind, func(t *testing.T) {
			b := &commandFixtureBackend{}
			if kind == "unknown" {
				b.commitErr = fixturePrivateError{}
			}
			registered := false
			grant := func(ctx context.Context, _ serialization.Action, _ serialization.Record) error {
				if kind == "callback" && !registered {
					registered = true
					return db.OnCommit(ctx, "fixtures", func(context.Context) error { return fixturePrivateError{} }, false)
				}
				return nil
			}
			f := fixtureCommandProvider(t, b, grant)
			w := &fixtureCommandTestWriter{}
			flags := []string{"--database=fixtures", "--format=json"}
			if kind == "dry_output" {
				flags = append(flags, "--dry-run")
				w.write = func([]byte) (int, error) { return 0, io.ErrClosedPipe }
			}
			run, _ := configureFixtureCommand(t, LoadDataCommand(commandFixtureResolver(f)), flags...)
			err := run(context.Background(), fixtureCommandInvocation(t, w), []string{"example.Note"})
			receipt := fixtureCommandError(t, err)
			switch kind {
			case "unknown":
				if !errors.Is(err, serialization.ErrOutcomeUnknown) || receipt.Load.Committed || w.calls != 0 {
					t.Fatal(err, receipt.Load, w.calls)
				}
			case "callback":
				if !errors.Is(err, serialization.ErrCommittedCallback) || !receipt.Load.Committed || w.calls != 0 {
					t.Fatal(err, receipt.Load, w.calls)
				}
			case "dry_output":
				if receipt.Load.Committed || !receipt.Load.DryRun || !errors.Is(err, io.ErrClosedPipe) || b.commits != 0 {
					t.Fatal(err, receipt.Load, b.commits)
				}
			}
		})
	}
}

func TestFixtureDumpFailureDoesNotAppendOrRetry(t *testing.T) {
	b := &commandFixtureBackend{rows: [][]any{{int64(1), "safe title"}}}
	f := fixtureCommandProvider(t, b, nil)
	w := &fixtureCommandTestWriter{write: func([]byte) (int, error) { return 0, io.ErrClosedPipe }}
	run, _ := configureFixtureCommand(t, DumpDataCommand(commandFixtureResolver(f)), "--database=fixtures", "--format=json")
	err := run(context.Background(), fixtureCommandInvocation(t, w), []string{"example.Note"})
	receipt := fixtureCommandError(t, err)
	if w.calls != 1 || receipt.Dump.Complete || !errors.Is(err, io.ErrClosedPipe) || b.commits != 0 {
		t.Fatal(w.calls, receipt.Dump, err)
	}
}

type fixtureCommandContext struct {
	context.Context
	hook     func()
	panicErr bool
}

func (c *fixtureCommandContext) Err() error {
	if c.panicErr {
		panic("private context")
	}
	if c.hook != nil {
		hook := c.hook
		c.hook = nil
		hook()
	}
	return c.Context.Err()
}
func TestFixtureCommandCapturesInvocationFlagsModelsAndProvider(t *testing.T) {
	b := &commandFixtureBackend{}
	f := fixtureCommandProvider(t, b, nil)
	replacement := fixtureCommandProvider(t, &commandFixtureBackend{}, nil)
	var output, replacementOutput bytes.Buffer
	inv := fixtureCommandInvocation(t, &output)
	registry := inv.Application.Registry
	models := []string{"example.Note"}
	ctx := &fixtureCommandContext{Context: context.Background()}
	var set *flag.FlagSet
	command := LoadDataCommand(func(_ context.Context, r *app.Registry, s conf.Values, alias string) (*serialization.Fixtures, error) {
		if r != registry || s.String("GOGO_FIXTURE_MARKER") != "original" || alias != "fixtures" {
			t.Fatal("invocation retargeted")
		}
		ctx.hook = func() { *f = *replacement }
		return f, nil
	})
	run, flags := configureFixtureCommand(t, command, "--database=fixtures", "--format=json")
	set = flags
	ctx.hook = func() {
		inv.Stdout = &replacementOutput
		inv.Stdin = strings.NewReader("invalid")
		inv.Application = &app.Application{}
		inv.Settings = conf.Values{}
		models[0] = "replacement.Note"
		_ = set.Set("database", "replacement")
		_ = set.Set("format", "jsonl")
		_ = set.Set("dry-run", "true")
	}
	if err := run(ctx, inv, models); err != nil || b.commits != 1 || !strings.Contains(output.String(), `"committed":true`) || replacementOutput.Len() != 0 {
		t.Fatal(err, b.commits, output.String())
	}
}

func TestFixtureCommandInvalidDirectCallsAndPrivateResolverErrors(t *testing.T) {
	for _, mode := range []string{"nil_context", "typed_nil", "panic_context", "canceled", "nil_invocation", "nil_writer", "nil_reader", "nil_resolver", "nil_provider", "alias", "foreign_commit", "panic_resolver", "missing_flags", "missing_models"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			inv := fixtureCommandInvocation(t, &out)
			var ctx context.Context = context.Background()
			calls := 0
			f := fixtureCommandProvider(t, &commandFixtureBackend{}, nil)
			resolve := func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
				calls++
				switch mode {
				case "nil_provider":
					return nil, nil
				case "foreign_commit":
					return nil, errors.Join(fixturePrivateError{}, serialization.ErrCommittedCallback)
				case "panic_resolver":
					panic(fixturePrivateError{})
				}
				return f, nil
			}
			flags := []string{"--database=fixtures", "--format=json"}
			args := []string{"example.Note"}
			switch mode {
			case "nil_context":
				ctx = nil
			case "typed_nil":
				ctx = (*fixtureCommandContext)(nil)
			case "panic_context":
				ctx = &fixtureCommandContext{Context: context.Background(), panicErr: true}
			case "canceled":
				c, cancel := context.WithCancel(context.Background())
				cancel()
				ctx = c
			case "nil_invocation":
				inv = nil
			case "nil_writer":
				inv.Stdout = (*bytes.Buffer)(nil)
			case "nil_reader":
				inv.Stdin = (*strings.Reader)(nil)
			case "nil_resolver":
				resolve = nil
			case "alias":
				flags[0] = "--database=other"
			case "missing_flags":
				flags = nil
			case "missing_models":
				args = nil
			}
			run, _ := configureFixtureCommand(t, LoadDataCommand(resolve), flags...)
			err := run(ctx, inv, args)
			if err == nil || out.Len() != 0 {
				t.Fatal("invalid call succeeded", mode)
			}
			if mode == "foreign_commit" {
				receipt := fixtureCommandError(t, err)
				if receipt.Load.Committed || !errors.Is(err, serialization.ErrCommittedCallback) {
					t.Fatal("foreign result was promoted")
				}
				var private fixturePrivateError
				if errors.As(err, &private) {
					t.Fatal("private resolver cause retained")
				}
			}
			if mode == "nil_context" || mode == "typed_nil" || mode == "panic_context" || mode == "canceled" {
				if calls != 0 {
					t.Fatal("context failure reached resolver")
				}
			}
		})
	}
}

type fixtureErrorGraph struct{ panicUnwrap bool }

func (*fixtureErrorGraph) Error() string { panic("must not format") }
func (e *fixtureErrorGraph) Unwrap() error {
	if e.panicUnwrap {
		panic("private unwrap")
	}
	return e
}
func TestFixtureCommandCauseProjectionIsBoundedAndSafe(t *testing.T) {
	for _, cause := range []error{&fixtureErrorGraph{}, &fixtureErrorGraph{panicUnwrap: true}, fixturePrivateError{}, errors.Join(fixturePrivateError{}, context.Canceled, serialization.ErrOutcomeUnknown)} {
		err := fixtureCommandCause(cause)
		if !errors.Is(err, serialization.ErrUnavailable) {
			t.Fatal("missing safe fallback")
		}
		var raw *fixtureErrorGraph
		if errors.As(err, &raw) {
			t.Fatal("raw graph retained")
		}
	}
	err := fixtureCommandCause(errors.Join(context.Canceled, serialization.ErrOutcomeUnknown))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, serialization.ErrOutcomeUnknown) {
		t.Fatal("safe identities lost")
	}
}

func TestFixtureCommandConfigureHasNoCrossInvocationState(t *testing.T) {
	command := LoadDataCommand(func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
		return nil, serialization.ErrForbidden
	})
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			run, _ := configureFixtureCommand(t, command, "--database=fixtures", "--format=json")
			inv := fixtureCommandInvocation(t, io.Discard)
			err := run(context.Background(), inv, []string{"example.Note"})
			if !errors.Is(err, serialization.ErrForbidden) {
				t.Error("per-invocation selection lost")
			}
		}()
	}
	group.Wait()
}

func TestFixtureCommandCompletionReceiptSurvivesCleanupAndWrapperPanic(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, mode := range []string{"close_error", "close_panic", "close_cancel", "wrapper_panic", "close_retarget", "dry_close"} {
		t.Run(mode, func(t *testing.T) {
			b := &commandFixtureBackend{}
			f := fixtureCommandProvider(t, b, nil)
			command := LoadDataCommand(commandFixtureResolver(f))
			command.OpenResources = true
			configure := command.Configure
			var captured context.Context
			command.Configure = func(flags *flag.FlagSet) Runner {
				run := configure(flags)
				return func(ctx context.Context, i *Invocation, args []string) error {
					captured = ctx
					err := run(ctx, i, args)
					if err == nil && mode == "wrapper_panic" {
						panic("private wrapper")
					}
					return err
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			closed := 0
			project := Project{Root: t.TempDir(), Commands: []Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) {
				return []app.Resource{{Name: "receipt", Open: func(context.Context) (func(context.Context) error, error) {
					return func(context.Context) error {
						closed++
						switch mode {
						case "close_panic":
							panic("private close")
						case "close_cancel":
							cancel()
							return nil
						case "wrapper_panic":
							return nil
						case "close_retarget":
							captured.Value(fixtureCommandCompletionKey{}).(*fixtureCommandCompletion).record(FixtureCommandError{})
						}
						return fixturePrivateError{}
					}, nil
				}}}, nil
			}}
			args := []string{"loaddata", "example.Note", "--database=fixtures", "--format=json"}
			if mode == "dry_close" {
				args = append(args, "--dry-run")
			}
			var stdout bytes.Buffer
			err := Call(ctx, project, args, Options{Stdin: strings.NewReader(fixtureCommandInput), Stdout: &stdout, Stderr: io.Discard})
			receipt := fixtureCommandError(t, err)
			if closed != 1 || receipt.Load.Records != 1 || receipt.Load.Committed != (mode != "dry_close") || receipt.Load.DryRun != (mode == "dry_close") || strings.Count(stdout.String(), "\n") != 1 {
				t.Fatal(receipt.Load, closed, stdout.String())
			}
			var private fixturePrivateError
			if errors.As(err, &private) {
				t.Fatal("raw close error retained")
			}
			if mode == "close_cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("late cancellation lost")
			}
		})
	}
}

func TestFixtureCommandWrapperCannotReplaceFirstCompletion(t *testing.T) {
	isolateCommandEnvironment(t)
	b := &commandFixtureBackend{}
	f := fixtureCommandProvider(t, b, nil)
	resolved := 0
	command := LoadDataCommand(func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
		resolved++
		return f, nil
	})
	configure := command.Configure
	command.Configure = func(flags *flag.FlagSet) Runner {
		run := configure(flags)
		return func(ctx context.Context, i *Invocation, args []string) error {
			if err := run(ctx, i, args); err != nil {
				return err
			}
			i.Stdin = strings.NewReader(fixtureCommandInput)
			return run(ctx, i, args)
		}
	}
	var output bytes.Buffer
	project := Project{Root: t.TempDir(), Commands: []Command{command}}
	err := Call(context.Background(), project, []string{"loaddata", "--database=fixtures", "--format=json", "example.Note"}, Options{Stdin: strings.NewReader(fixtureCommandInput), Stdout: &output, Stderr: io.Discard})
	receipt := fixtureCommandError(t, err)
	if !receipt.Load.Committed || receipt.Load.Records != 1 || resolved != 1 || b.inserts != 1 || b.commits != 1 || strings.Count(output.String(), "\n") != 1 {
		t.Fatal(receipt.Load, resolved, b.inserts, b.commits, output.String())
	}
}

func TestFixtureCommandCallFreezesSelectionBeforeResources(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, phase := range []string{"validate", "resource_factory", "ready"} {
		t.Run(phase, func(t *testing.T) {
			b := &commandFixtureBackend{}
			f := fixtureCommandProvider(t, b, nil)
			command := LoadDataCommand(func(_ context.Context, _ *app.Registry, _ conf.Values, alias string) (*serialization.Fixtures, error) {
				if alias != "fixtures" {
					t.Fatal("post-parse alias changed")
				}
				return f, nil
			})
			command.OpenResources = true
			configure := command.Configure
			var parsed *flag.FlagSet
			command.Configure = func(f *flag.FlagSet) Runner { parsed = f; return configure(f) }
			mutate := func() {
				_ = parsed.Set("database", "replacement")
				_ = parsed.Set("format", "jsonl")
				_ = parsed.Set("dry-run", "true")
				parsed.Args()[0] = "replacement.Note"
			}
			if phase == "validate" {
				command.Validate = func([]string) error { mutate(); return nil }
			}
			project := Project{Root: t.TempDir(), Commands: []Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) {
				if phase == "resource_factory" {
					mutate()
				}
				return nil, nil
			}}
			if phase == "ready" {
				project.Apps = []app.Config{{Name: "fixture", Label: "fixture", Ready: func(context.Context, *app.Registry) error { mutate(); return nil }}}
			}
			var output bytes.Buffer
			err := Call(context.Background(), project, []string{"loaddata", "--database=fixtures", "--format=json", "example.Note"}, Options{Stdin: strings.NewReader(fixtureCommandInput), Stdout: &output, Stderr: io.Discard})
			if err != nil || b.commits != 1 || output.String() != "{\"records\":1,\"committed\":true,\"dry_run\":false}\n" {
				t.Fatal(err, b.commits, output.String())
			}
		})
	}
}

func TestFixtureCommandHelpUsesZeroSafeFlagValue(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, command := range []Command{DumpDataCommand(nil), LoadDataCommand(nil)} {
		var stdout, stderr bytes.Buffer
		project := Project{Root: t.TempDir(), Commands: []Command{command}}
		if err := Call(context.Background(), project, []string{command.Name, "--help"}, Options{Stdout: &stdout, Stderr: &stderr}); err != nil {
			t.Fatal(err)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "-database") || !strings.Contains(stderr.String(), "-format") || strings.Contains(stderr.String(), "panic") || strings.Contains(stderr.String(), "fixtureCommandFlag") {
			t.Fatal(stdout.String(), stderr.String())
		}
	}
	if (*fixtureCommandFlag)(nil).String() != "" || (&fixtureCommandFlag{}).String() != "" {
		t.Fatal("zero flag is not safe")
	}
}

func TestFixtureCommandCallRefusesRemovedSelectionMarker(t *testing.T) {
	isolateCommandEnvironment(t)
	resolved := 0
	command := LoadDataCommand(func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
		resolved++
		return nil, nil
	})
	configure := command.Configure
	command.Configure = func(flags *flag.FlagSet) Runner {
		run := configure(flags)
		value := flags.Lookup("database").Value.(*fixtureCommandFlag)
		flags.Lookup("database").Value = value.Value
		return run
	}
	project := Project{Root: t.TempDir(), Commands: []Command{command}}
	err := Call(context.Background(), project, []string{"loaddata", "--database=fixtures", "--format=json", "example.Note"}, Options{Stdin: strings.NewReader(fixtureCommandInput), Stdout: io.Discard, Stderr: io.Discard})
	if !errors.Is(err, serialization.ErrConfiguration) || resolved != 0 {
		t.Fatal(err, resolved)
	}
}

func TestFixtureCommandCleanupCannotEraseKnownUnknownOutcome(t *testing.T) {
	isolateCommandEnvironment(t)
	b := &commandFixtureBackend{commitErr: fixturePrivateError{}}
	f := fixtureCommandProvider(t, b, nil)
	command := LoadDataCommand(commandFixtureResolver(f))
	command.OpenResources = true
	project := Project{Root: t.TempDir(), Commands: []Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) {
		return []app.Resource{{Name: "receipt", Open: func(context.Context) (func(context.Context) error, error) {
			return func(context.Context) error { return &fixtureErrorGraph{} }, nil
		}}}, nil
	}}
	var output bytes.Buffer
	err := Call(context.Background(), project, []string{"loaddata", "--database=fixtures", "--format=json", "example.Note"}, Options{Stdin: strings.NewReader(fixtureCommandInput), Stdout: &output, Stderr: io.Discard})
	receipt := fixtureCommandError(t, err)
	if !errors.Is(err, serialization.ErrOutcomeUnknown) || receipt.Load.Committed || b.commits != 1 || output.Len() != 0 {
		t.Fatal("cleanup erased known uncertain commit", err, receipt.Load, b.commits, output.String())
	}
	var raw *fixtureErrorGraph
	if errors.As(err, &raw) {
		t.Fatal("private cyclic close graph retained")
	}
}

type fixtureCommandInvalidContext struct {
	context.Context
	returned error
}

func (c fixtureCommandInvalidContext) Err() error { return c.returned }

type fixtureCommandUnwrapTripwire struct{ calls *int }

func (*fixtureCommandUnwrapTripwire) Error() string   { panic("must not format context error") }
func (e *fixtureCommandUnwrapTripwire) Unwrap() error { *e.calls++; return e }

func TestFixtureCommandContextErrorsAreExactAndDoNotTraverse(t *testing.T) {
	for _, returned := range []error{nil, context.Canceled, context.DeadlineExceeded} {
		if got := fixtureCommandContextError(fixtureCommandInvalidContext{context.Background(), returned}); got != returned {
			t.Fatal("valid context outcome changed")
		}
	}
	calls := 0
	if got := fixtureCommandContextError(fixtureCommandInvalidContext{context.Background(), &fixtureCommandUnwrapTripwire{&calls}}); got != serialization.ErrUnavailable || calls != 0 {
		t.Fatal("malformed context error traversed", calls)
	}
	if got := fixtureCommandContextError(fixtureCommandInvalidContext{context.Background(), errors.Join(context.Canceled)}); got != serialization.ErrUnavailable {
		t.Fatal("nonconforming joined context error accepted")
	}
}
