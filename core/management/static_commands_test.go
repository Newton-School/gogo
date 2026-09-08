package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/static"
)

func staticCommandProject(t *testing.T, configured bool) (Project, string, *int) {
	t.Helper()
	isolateCommandEnvironment(t)
	destination := filepath.Join(t.TempDir(), "collected")
	resolved := 0
	project := Project{Root: t.TempDir(), Environment: map[string]string{}}
	if configured {
		project.Environment["GOGO_STATIC_ROOT"] = destination
	}
	project.Apps = []app.Config{{Name: "example.assets", Label: "assets", Register: func(registry *app.Registry) error {
		return static.Register(registry, "assets", static.Source{FS: fstest.MapFS{"app.txt": {Data: []byte("app")}, "shared.txt": {Data: []byte("app")}}})
	}, Ready: func(context.Context, *app.Registry) error { t.Fatal("static command ran Ready"); return nil }}}
	project.ResourceFactory = func(conf.Values, []string) ([]app.Resource, error) {
		t.Fatal("static command opened services")
		return nil, nil
	}
	configs := append([]app.Config(nil), project.Apps...)
	project.Commands = StaticCommands(func(ctx context.Context, registry *app.Registry, settings conf.Values) (*static.Collector, error) {
		resolved++
		sources, err := static.AppSources(registry, configs)
		if err != nil {
			return nil, err
		}
		sources = append([]static.Source{{Owner: "project", FS: fstest.MapFS{"shared.txt": {Data: []byte("project")}}}}, sources...)
		return static.New(static.Config{Sources: sources, Destination: settings.String("GOGO_STATIC_ROOT"), BaseURL: "/static/"})
	})
	return project, destination, &resolved
}

func TestStaticCommandsDirectLifecycleFindDryRunAndCollect(t *testing.T) {
	project, destination, resolved := staticCommandProject(t, true)
	for _, test := range []struct {
		args           []string
		dry, published bool
	}{{[]string{"findstatic", "shared.txt"}, false, false}, {[]string{"collectstatic", "--dry-run"}, true, false}, {[]string{"collectstatic"}, false, true}} {
		var output bytes.Buffer
		if err := Call(context.Background(), project, test.args, Options{Stdout: &output}); err != nil {
			t.Fatal(err)
		}
		var result staticCommandResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.DryRun != test.dry || result.Published != test.published || len(result.Matches) < 2 || result.Matches[0].Owner != "project" || !result.Matches[0].Selected {
			t.Fatal(output.String())
		}
		if strings.Contains(output.String(), destination) {
			t.Fatal("absolute path in public output")
		}
		_, err := os.Stat(filepath.Join(destination, static.ManifestName))
		if test.published && err != nil || !test.published && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(test, err)
		}
	}
	if *resolved != 3 {
		t.Fatal(*resolved)
	}
}

func TestStaticCommandFlagsAndConfigurationFailBeforeResolver(t *testing.T) {
	for _, args := range [][]string{{"collectstatic", "--clear"}, {"collectstatic", "extra"}, {"findstatic"}, {"findstatic", "a", "b"}, {"collectstatic", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			project, _, resolved := staticCommandProject(t, false)
			var output, diagnostics bytes.Buffer
			code := Run(context.Background(), project, append([]string{"manage"}, args...), Options{Stdout: &output, Stderr: &diagnostics})
			want := 2
			if args[len(args)-1] == "--help" {
				want = 0
			}
			if code != want || *resolved != 0 {
				t.Fatal(code, *resolved, diagnostics.String())
			}
		})
	}
	project, _, resolved := staticCommandProject(t, false)
	var output bytes.Buffer
	if err := Call(context.Background(), project, []string{"collectstatic", "--dry-run"}, Options{Stdout: &output}); err == nil || *resolved != 0 || output.Len() != 0 {
		t.Fatal(err, *resolved)
	}
	if err := Call(context.Background(), project, []string{"findstatic", "app.txt"}, Options{Stdout: &output}); err != nil || *resolved != 1 {
		t.Fatal(err, *resolved)
	}
}

type staticCommandWriter struct {
	calls    int
	callback func([]byte) (int, error)
}

func (w *staticCommandWriter) Write(p []byte) (int, error) { w.calls++; return w.callback(p) }

func TestStaticCommandsIncompleteOutputRetainsPublishedOutcome(t *testing.T) {
	for _, mode := range []string{"short", "overcount", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			project, destination, _ := staticCommandProject(t, true)
			writer := &staticCommandWriter{callback: func(p []byte) (int, error) {
				switch mode {
				case "short":
					return len(p) - 1, nil
				case "overcount":
					return len(p) + 1, nil
				case "panic":
					panic("private writer failure")
				default:
					return 0, errors.New("private writer failure")
				}
			}}
			err := Call(context.Background(), project, []string{"collectstatic"}, Options{Stdout: writer})
			if err == nil || !strings.Contains(err.Error(), "manifest published") || strings.Contains(err.Error(), "private") || writer.calls != 1 {
				t.Fatal(err, writer.calls)
			}
			if mode == "short" || mode == "overcount" {
				if !errors.Is(err, io.ErrShortWrite) {
					t.Fatal(err)
				}
			}
			if _, err := static.LoadManifest(context.Background(), destination, "/static/"); err != nil {
				t.Fatal("published manifest missing", err)
			}
		})
	}
}

func TestStaticCommandSnapshotsFlagsWriterArgumentsAndCollector(t *testing.T) {
	c, err := static.New(static.Config{Sources: []static.Source{{Owner: "original", FS: fstest.MapFS{"one.txt": {Data: []byte("one")}}}}, Destination: filepath.Join(t.TempDir(), "collected"), BaseURL: "/static/"})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.Prepare(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var original, replacement bytes.Buffer
	invocation := &Invocation{Application: application, Stdout: &original}
	flags := flag.NewFlagSet("collectstatic", flag.ContinueOnError)
	command := StaticCommands(func(context.Context, *app.Registry, conf.Values) (*static.Collector, error) {
		invocation.Stdout = &replacement
		if err := flags.Set("dry-run", "false"); err != nil {
			t.Fatal(err)
		}
		return c, nil
	})[0]
	run := command.Configure(flags)
	if err := flags.Parse([]string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), invocation, nil); err != nil {
		t.Fatal(err)
	}
	var result staticCommandResult
	if err := json.Unmarshal(original.Bytes(), &result); err != nil || !result.DryRun || result.Published || replacement.Len() != 0 {
		t.Fatal(result, err)
	}
}

func TestStaticCommandResolverErrorsNeverWrite(t *testing.T) {
	for _, mode := range []string{"error", "panic", "nil", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			commands := StaticCommands(func(context.Context, *app.Registry, conf.Values) (*static.Collector, error) {
				switch mode {
				case "error":
					return nil, errors.New("private configuration")
				case "panic":
					panic("private configuration")
				case "cancel":
					cancel()
				}
				return nil, nil
			})
			application, err := app.Prepare(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			run := commands[1].Configure(flag.NewFlagSet("findstatic", flag.ContinueOnError))
			err = run(ctx, &Invocation{Application: application, Stdout: &output}, []string{"one.txt"})
			if err == nil || output.Len() != 0 || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
