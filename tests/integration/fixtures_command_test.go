package integration_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/management"
	"github.com/Newton-School/gogo/core/serialization"
)

// Read the marked provider configuration before calling this helper. Management
// project settings must not inherit unrelated GOGO_TEST_* service controls.
func isolateFixtureCLIEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestFixturesCommandsNativeRoundTripAndDryRun(t *testing.T) {
	for _, format := range []serialization.Format{serialization.JSON, serialization.JSONL} {
		t.Run(string(format), func(t *testing.T) {
			b, schema, profile := nativeSmallFixture(t)
			nativeFixtureExec(t, b, `INSERT INTO fixture_documents VALUES ('one',1,'Public',true),('two',2,'Private',true)`)
			source := nativeFixtures(t, b, profile)
			copySchema := schema.Clone()
			copySchema.Table = "fixture_cli_copies"
			if err := b.SchemaEditor().CreateModel(context.Background(), b, copySchema); err != nil {
				t.Fatal(err)
			}
			target := nativeFixtures(t, b, nativeFixtureProfile(t, b, copySchema, true))
			isolateFixtureCLIEnvironment(t)
			selected := source
			resolve := func(_ context.Context, registry *app.Registry, _ conf.Values, alias string) (*serialization.Fixtures, error) {
				if registry == nil || alias != b.Alias() {
					return nil, serialization.ErrConfiguration
				}
				return selected, nil
			}
			project := management.Project{Root: t.TempDir(), Commands: []management.Command{management.DumpDataCommand(resolve), management.LoadDataCommand(resolve)}}
			selection := []string{"--database=" + b.Alias(), "--format=" + string(format), schema.Key()}
			var fixture, direct bytes.Buffer
			if _, err := source.Dump(context.Background(), &direct, serialization.DumpOptions{Format: format, Models: []string{schema.Key()}}); err != nil {
				t.Fatal(err)
			}
			if err := management.Call(context.Background(), project, append([]string{"dumpdata"}, selection...), management.Options{Stdout: &fixture, Stderr: io.Discard}); err != nil || !bytes.Equal(fixture.Bytes(), direct.Bytes()) || bytes.Contains(fixture.Bytes(), []byte("Private")) {
				t.Fatal("CLI dump diverged from scoped serializer", err)
			}
			selected = target
			for _, dry := range []bool{true, false} {
				args := append([]string{"loaddata"}, selection...)
				if dry {
					args = append(args, "--dry-run")
				}
				var summary bytes.Buffer
				if err := management.Call(context.Background(), project, args, management.Options{Stdin: bytes.NewReader(fixture.Bytes()), Stdout: &summary, Stderr: io.Discard}); err != nil {
					t.Fatal(err)
				}
				want := `{"records":1,"committed":true,"dry_run":false}` + "\n"
				count := 1
				if dry {
					want = `{"records":1,"committed":false,"dry_run":true}` + "\n"
					count = 0
				}
				if summary.String() != want || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_cli_copies`) != count {
					t.Fatal("load summary disagreed with database", summary.String())
				}
			}
			var duplicate bytes.Buffer
			err := management.Call(context.Background(), project, append([]string{"loaddata"}, selection...), management.Options{Stdin: bytes.NewReader(fixture.Bytes()), Stdout: &duplicate, Stderr: io.Discard})
			var receipt *management.FixtureCommandError
			if !errors.As(err, &receipt) || receipt.Load.Committed || duplicate.Len() != 0 || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_cli_copies`) != 1 {
				t.Fatal("duplicate CLI load was not refused atomically", err)
			}
		})
	}
}

type fixtureCLIFailingWriter struct{ calls int }

func (w *fixtureCLIFailingWriter) Write([]byte) (int, error) { w.calls++; return 0, io.ErrClosedPipe }
func TestFixturesCommandsNativeCommittedCompletionFailures(t *testing.T) {
	for _, mode := range []string{"writer", "closer"} {
		t.Run(mode, func(t *testing.T) {
			b, schema, profile := nativeSmallFixture(t)
			fixtures := nativeFixtures(t, b, profile)
			input := nativeSmallFixtureInput(t, 1)
			isolateFixtureCLIEnvironment(t)
			command := management.LoadDataCommand(func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
				return fixtures, nil
			})
			command.OpenResources = true
			closed := 0
			project := management.Project{Root: t.TempDir(), Commands: []management.Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) {
				return []app.Resource{{Name: "fixture_cli_test", Open: func(context.Context) (func(context.Context) error, error) {
					return func(context.Context) error {
						closed++
						if mode == "closer" {
							return errors.New("private fixture closer details")
						}
						return nil
					}, nil
				}}}, nil
			}}
			var output bytes.Buffer
			var writer io.Writer = &output
			failure := &fixtureCLIFailingWriter{}
			if mode == "writer" {
				writer = failure
			}
			err := management.Call(context.Background(), project, []string{"loaddata", "--database=" + b.Alias(), "--format=json", schema.Key()}, management.Options{Stdin: bytes.NewReader(input), Stdout: writer, Stderr: io.Discard})
			var receipt *management.FixtureCommandError
			if !errors.As(err, &receipt) || !receipt.Load.Committed || receipt.Load.Records != 1 || closed != 1 || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_documents`) != 1 {
				t.Fatal("confirmed native commit lost after completion failure", err, closed)
			}
			if strings.Contains(err.Error(), "private") || mode == "writer" && (!errors.Is(err, io.ErrClosedPipe) || failure.calls != 1) || mode == "closer" && output.String() != `{"records":1,"committed":true,"dry_run":false}`+"\n" {
				t.Fatal("unsafe or repeated CLI completion", err)
			}
		})
	}
}

func TestFixturesCommandsNativeDenialAndPreResourceFlags(t *testing.T) {
	b, schema, profile := nativeSmallFixture(t)
	profile.Authorize = func(context.Context, serialization.Action, serialization.Record) error {
		return serialization.ErrForbidden
	}
	fixtures := nativeFixtures(t, b, profile)
	isolateFixtureCLIEnvironment(t)
	opened := 0
	command := management.LoadDataCommand(func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
		return fixtures, nil
	})
	command.OpenResources = true
	project := management.Project{Root: t.TempDir(), Commands: []management.Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { opened++; return nil, nil }}
	for _, args := range [][]string{{"loaddata", "--help"}, {"loaddata", "--format=json", schema.Key()}, {"loaddata", "--database=" + b.Alias(), "--format=invalid", schema.Key()}, {"loaddata", "--database=" + b.Alias(), "--format=json"}} {
		_ = management.Call(context.Background(), project, args, management.Options{Stdin: bytes.NewReader(nativeSmallFixtureInput(t, 1)), Stdout: io.Discard, Stderr: io.Discard})
		if opened != 0 {
			t.Fatal("invalid CLI opened resources")
		}
	}
	var output bytes.Buffer
	err := management.Call(context.Background(), project, []string{"loaddata", "--database=" + b.Alias(), "--format=json", schema.Key()}, management.Options{Stdin: bytes.NewReader(nativeSmallFixtureInput(t, 1)), Stdout: &output, Stderr: io.Discard})
	if !errors.Is(err, serialization.ErrForbidden) || opened != 1 || output.Len() != 0 || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_documents`) != 0 {
		t.Fatal("denied CLI import changed data", err, opened)
	}
}
