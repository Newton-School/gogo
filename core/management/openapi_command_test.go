package management

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/urls"
)

type openAPICommandBackend struct{ db.Backend }

func (*openAPICommandBackend) Alias() string       { panic("unexpected backend call") }
func (*openAPICommandBackend) Dialect() db.Dialect { panic("unexpected backend call") }

func openAPICommandRouter(t *testing.T) *urls.Router {
	t.Helper()
	registry := &models.Registry{}
	if err := registry.Register(models.Schema{AppLabel: "catalog", Name: "Book", Fields: []models.Field{
		models.BigAutoField("id"), models.TextField("title"), models.TextField("private_value"),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	serializer, err := api.New(api.Definition{Fields: []api.Field{api.IntegerField("id"), api.StringField("title")}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := api.NewResource(api.ResourceConfig{
		Store: orm.New(&openAPICommandBackend{}, registry), Model: "catalog.Book", Serializer: serializer,
		Policy: auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { panic("unexpected policy sample") }),
		Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
			panic("unexpected scope sample")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := resource.ReadOpenAPIRoutes("books/", "book", api.ReadOpenAPIOptions{Security: []api.OpenAPISecurity{{Type: "bearer"}}})
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Include("v1/", "v1", routes...))
	if err != nil {
		t.Fatal(err)
	}
	return router
}

var openAPICommandOptions = api.OpenAPIOptions{Title: "Catalog API", Version: "1.0.0"}

func openAPICommandSettings(t *testing.T, marker string) conf.Values {
	t.Helper()
	settings, err := (conf.Schema{{Name: "GOGO_SCHEMA_MARKER", Default: marker}}).Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func openAPICommandInvocation(t *testing.T, stdout io.Writer) *Invocation {
	t.Helper()
	application, err := app.Prepare(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Invocation{Application: application, Settings: openAPICommandSettings(t, "original"), Stdout: stdout}
}

func openAPICommandProject(t *testing.T, command Command) Project {
	t.Helper()
	isolateCommandEnvironment(t)
	return Project{Root: t.TempDir(), Schema: conf.Schema{{Name: "GOGO_SCHEMA_MARKER", Default: "original"}}, Commands: []Command{command}}
}

type openAPICommandWriter struct {
	bytes.Buffer
	calls int
	write func([]byte) (int, error)
}

func (w *openAPICommandWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.write != nil {
		return w.write(p)
	}
	return w.Buffer.Write(p)
}

func TestOpenAPICommandEmitsSameDocumentWithoutImplicitResources(t *testing.T) {
	router := openAPICommandRouter(t)
	want, err := api.OpenAPI(context.Background(), router, openAPICommandOptions)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	options := openAPICommandOptions
	command := OpenAPICommand(func(ctx context.Context, registry *app.Registry, settings conf.Values) (*urls.Router, error) {
		calls++
		if ctx.Err() != nil || registry == nil || settings.String("GOGO_SCHEMA_MARKER") != "original" {
			t.Fatal("resolver did not receive invocation dependencies")
		}
		return router, nil
	}, options)
	options.Title = "replacement"
	if command.Name != "openapi" || command.OpenResources || len(command.Resources) != 0 {
		t.Fatal("implicit resource registration", command)
	}
	project := openAPICommandProject(t, command)
	project.ResourceFactory = func(conf.Values, []string) ([]app.Resource, error) {
		t.Fatal("implicit resources opened")
		return nil, nil
	}
	var stdout openAPICommandWriter
	if err := Call(context.Background(), project, []string{"openapi"}, Options{Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || stdout.calls != 1 || !bytes.Equal(stdout.Bytes(), want) || bytes.Contains(stdout.Bytes(), []byte("private_value")) {
		t.Fatal("emission differs from public generator", calls, stdout.calls)
	}
}

func TestOpenAPICommandFlagsPrecedeResolverAndResourceStartup(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"extra"}, {"--title", "override"}, {"--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { calls++; return nil, nil }, openAPICommandOptions)
			command.Resources, command.OpenResources = []string{"catalog"}, true
			project := openAPICommandProject(t, command)
			project.ResourceFactory = func(conf.Values, []string) ([]app.Resource, error) { calls++; return nil, nil }
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), project, append([]string{"manage", "openapi"}, args...), Options{Stdout: &stdout, Stderr: &stderr})
			want := 2
			if args[0] == "--help" {
				want = 0
			}
			if code != want || calls != 0 || stdout.Len() != 0 {
				t.Fatal(code, calls, stdout.String(), stderr.String())
			}
		})
	}
}

func TestOpenAPICommandClosesOnlyExplicitResourcesOnFailure(t *testing.T) {
	for _, mode := range []string{"success", "resolve", "panic", "write"} {
		t.Run(mode, func(t *testing.T) {
			router := openAPICommandRouter(t)
			var events []string
			private := errors.New("private-provider-detail")
			command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) {
				events = append(events, "resolve")
				if mode == "panic" {
					panic(private)
				}
				if mode == "resolve" {
					return router, private
				}
				return router, nil
			}, openAPICommandOptions)
			command.Resources, command.OpenResources = []string{"catalog"}, true
			project := openAPICommandProject(t, command)
			project.ResourceFactory = func(_ conf.Values, selected []string) ([]app.Resource, error) {
				if !reflect.DeepEqual(selected, []string{"catalog"}) {
					t.Fatal("unrelated resource selection", selected)
				}
				return []app.Resource{{Name: "catalog", Open: func(context.Context) (func(context.Context) error, error) {
					events = append(events, "open")
					return func(context.Context) error { events = append(events, "close"); return nil }, nil
				}}}, nil
			}
			var stdout openAPICommandWriter
			if mode == "write" {
				stdout.write = func([]byte) (int, error) { return 0, private }
			}
			var stderr bytes.Buffer
			code := Run(context.Background(), project, []string{"manage", "openapi"}, Options{Stdout: &stdout, Stderr: &stderr})
			want := 1
			if mode == "success" {
				want = 0
			}
			if code != want || !reflect.DeepEqual(events, []string{"open", "resolve", "close"}) || strings.Contains(stderr.String(), private.Error()) {
				t.Fatal(code, events, stderr.String())
			}
			if mode != "success" && stdout.Len() != 0 {
				t.Fatal("failure emitted partial schema")
			}
		})
	}
}

type openAPICommandContext struct {
	context.Context
	check func()
}

func (c *openAPICommandContext) Err() error {
	if c.check != nil {
		c.check()
	}
	return c.Context.Err()
}

func TestOpenAPICommandSnapshotsInvocationBeforeContextAndResolver(t *testing.T) {
	router := openAPICommandRouter(t)
	for _, point := range []string{"context", "resolver"} {
		t.Run(point, func(t *testing.T) {
			var original, replacement openAPICommandWriter
			invocation := openAPICommandInvocation(t, &original)
			registry := invocation.Application.Registry
			other := openAPICommandInvocation(t, &replacement)
			other.Settings = openAPICommandSettings(t, "replacement")
			change := func() { *invocation = *other }
			ctx := &openAPICommandContext{Context: context.Background()}
			if point == "context" {
				ctx.check = change
			}
			command := OpenAPICommand(func(_ context.Context, got *app.Registry, settings conf.Values) (*urls.Router, error) {
				if got != registry || settings.String("GOGO_SCHEMA_MARKER") != "original" {
					t.Fatal("callback retargeted invocation dependencies")
				}
				change()
				return router, nil
			}, openAPICommandOptions)
			if err := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(ctx, invocation, nil); err != nil {
				t.Fatal(err)
			}
			if original.calls != 1 || original.Len() == 0 || replacement.calls != 0 {
				t.Fatal("callback retargeted publication", original.calls, replacement.calls)
			}
		})
	}
}

func TestOpenAPICommandCapturesResolvedRouterBeforeContextCallback(t *testing.T) {
	router := openAPICommandRouter(t)
	want, err := api.OpenAPI(context.Background(), router, openAPICommandOptions)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &openAPICommandContext{Context: context.Background()}
	command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) {
		ctx.check = func() { *router = urls.Router{} }
		return router, nil
	}, openAPICommandOptions)
	var stdout openAPICommandWriter
	err = command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(ctx, openAPICommandInvocation(t, &stdout), nil)
	if err != nil || stdout.calls != 1 || !bytes.Equal(stdout.Bytes(), want) {
		t.Fatal("post-resolver context retargeted the selected router", err, stdout.calls)
	}
}

func TestOpenAPICommandCancellationAndProviderCauses(t *testing.T) {
	router := openAPICommandRouter(t)
	for _, cancelAt := range []int{1, 2, 3, 4, 5, 6} {
		t.Run(string(rune('0'+cancelAt)), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checks, calls := 0, 0
			wrapped := &openAPICommandContext{Context: ctx, check: func() {
				checks++
				if checks == cancelAt {
					cancel()
				}
			}}
			command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { calls++; return router, nil }, openAPICommandOptions)
			var stdout openAPICommandWriter
			err := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(wrapped, openAPICommandInvocation(t, &stdout), nil)
			if !errors.Is(err, context.Canceled) || cancelAt == 1 && calls != 0 || cancelAt < 6 && stdout.calls != 0 || cancelAt == 6 && stdout.calls != 1 {
				t.Fatal("cancellation crossed publication boundary", err, checks, calls, stdout.calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	private := errors.New("private-provider-cause")
	command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) {
		cancel()
		return router, private
	}, openAPICommandOptions)
	var stdout openAPICommandWriter
	err := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(ctx, openAPICommandInvocation(t, &stdout), nil)
	if !errors.Is(err, private) || !errors.Is(err, context.Canceled) || stdout.calls != 0 || strings.Contains(err.Error(), private.Error()) {
		t.Fatal("provider/context cause lost or exposed", err, stdout.calls)
	}
}

func TestOpenAPICommandPanicPreservesCancellationWithoutPrivateValue(t *testing.T) {
	router := openAPICommandRouter(t)
	for _, point := range []string{"resolver", "writer"} {
		t.Run(point, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stdout openAPICommandWriter
			command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) {
				if point == "resolver" {
					cancel()
					panic("private panic value")
				}
				return router, nil
			}, openAPICommandOptions)
			if point == "writer" {
				stdout.write = func([]byte) (int, error) { cancel(); panic("private panic value") }
			}
			err := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(ctx, openAPICommandInvocation(t, &stdout), nil)
			if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "private") || point == "resolver" && stdout.calls != 0 || point == "writer" && stdout.calls != 1 {
				t.Fatal("panic dropped cancellation or changed publication", err, stdout.calls)
			}
		})
	}
}

func TestOpenAPICommandRejectsInvalidDirectInvocationAndMetadata(t *testing.T) {
	router := openAPICommandRouter(t)
	var typedNil *openAPICommandContext
	for _, ctx := range []context.Context{nil, typedNil, &openAPICommandContext{Context: context.Background(), check: func() { panic("private context panic") }}} {
		calls := 0
		command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { calls++; return router, nil }, openAPICommandOptions)
		var stdout openAPICommandWriter
		err := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(ctx, openAPICommandInvocation(t, &stdout), nil)
		var public *CommandError
		if !errors.As(err, &public) || public.Code != 1 || calls != 0 || stdout.calls != 0 || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe direct context", err, calls, stdout.calls)
		}
	}
	for _, mode := range []string{"invocation", "application", "registry", "stdout", "typed stdout", "resolver", "arguments", "nil router", "opaque router", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			var stdout openAPICommandWriter
			invocation := openAPICommandInvocation(t, &stdout)
			options := openAPICommandOptions
			resolve := func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { return router, nil }
			var args []string
			switch mode {
			case "invocation":
				invocation = nil
			case "application":
				invocation.Application = nil
			case "registry":
				invocation.Application.Registry = nil
			case "stdout":
				invocation.Stdout = nil
			case "typed stdout":
				invocation.Stdout = (*openAPICommandWriter)(nil)
			case "resolver":
				resolve = nil
			case "arguments":
				args = []string{"extra"}
			case "nil router":
				resolve = func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { return nil, nil }
			case "opaque router":
				opaque, err := urls.New(urls.Path("opaque/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("sampled handler") }), "opaque", "GET"))
				if err != nil {
					t.Fatal(err)
				}
				resolve = func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { return opaque, nil }
			case "metadata":
				options.Title = ""
			}
			command := OpenAPICommand(resolve, options)
			err := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(context.Background(), invocation, args)
			var public *CommandError
			if !errors.As(err, &public) || stdout.calls != 0 || mode == "arguments" && public.Code != 2 || mode != "arguments" && public.Code != 1 {
				t.Fatal("invalid invocation reported success", err, stdout.calls)
			}
		})
	}
}

func TestOpenAPICommandWriterErrorsNeverRetry(t *testing.T) {
	router := openAPICommandRouter(t)
	private := errors.New("private writer failure")
	for _, mode := range []string{"short", "negative", "oversize", "error", "full error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			stdout := &openAPICommandWriter{write: func(p []byte) (int, error) {
				switch mode {
				case "short":
					return len(p) - 1, nil
				case "negative":
					return -1, nil
				case "oversize":
					return len(p) + 1, nil
				case "error":
					return 0, private
				case "full error":
					return len(p), private
				default:
					panic(private)
				}
			}}
			command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { return router, nil }, openAPICommandOptions)
			err := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))(context.Background(), openAPICommandInvocation(t, stdout), nil)
			var public *CommandError
			if !errors.As(err, &public) || public.Code != 1 || stdout.calls != 1 || strings.Contains(err.Error(), private.Error()) {
				t.Fatal("unsafe writer failure", err, stdout.calls)
			}
			if (mode == "short" || mode == "negative" || mode == "oversize" || mode == "error") && !errors.Is(err, io.ErrShortWrite) || (mode == "error" || mode == "full error") && !errors.Is(err, private) {
				t.Fatal("writer cause lost", err)
			}
		})
	}
}

func TestOpenAPICommandIndependentDirectRunners(t *testing.T) {
	router := openAPICommandRouter(t)
	want, err := api.OpenAPI(context.Background(), router, openAPICommandOptions)
	if err != nil {
		t.Fatal(err)
	}
	command := OpenAPICommand(func(context.Context, *app.Registry, conf.Values) (*urls.Router, error) { return router, nil }, openAPICommandOptions)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		var stdout openAPICommandWriter
		invocation := openAPICommandInvocation(t, &stdout)
		runner := command.Configure(flag.NewFlagSet("openapi", flag.ContinueOnError))
		group.Add(1)
		go func() {
			defer group.Done()
			if err := runner(context.Background(), invocation, nil); err != nil || stdout.calls != 1 || !bytes.Equal(stdout.Bytes(), want) {
				t.Error("independent command state changed", err)
			}
		}()
	}
	group.Wait()
}
