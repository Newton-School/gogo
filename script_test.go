package gogo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/management"
)

func cleanScriptEnvironment(t *testing.T) {
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

func TestScriptBootstrapsAndClosesProject(t *testing.T) {
	cleanScriptEnvironment(t)
	var events []string
	project := Project{
		Root:             t.TempDir(),
		Environment:      map[string]string{"GOGO_SCRIPT_LABEL": "maintenance"},
		Schema:           append(conf.CoreSchema(), conf.Definition{Name: "GOGO_SCRIPT_LABEL"}),
		RuntimeResources: []string{"custom"},
		Apps: []app.Config{{Name: "catalog", Label: "catalog", Register: func(r *app.Registry) error {
			events = append(events, "register")
			return r.Register("services", "message", "hello")
		}, Ready: func(context.Context, *app.Registry) error { events = append(events, "ready"); return nil }, Shutdown: func(context.Context) error { events = append(events, "shutdown"); return nil }}},
		ResourceFactory: func(_ conf.Values, names []string) ([]app.Resource, error) {
			if !reflect.DeepEqual(names, []string{"custom"}) {
				t.Fatal(names)
			}
			return []app.Resource{{Name: "custom", Open: func(context.Context) (func(context.Context) error, error) {
				events = append(events, "open")
				return func(context.Context) error { events = append(events, "close"); return nil }, nil
			}}}, nil
		},
		Handler: func(*app.Registry, conf.Values) (http.Handler, error) {
			t.Fatal("script started HTTP")
			return nil, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := runScript(context.Background(), project, func(ctx context.Context, i *management.Invocation, args []string) error {
		events = append(events, "run")
		if ctx.Err() != nil || !reflect.DeepEqual(args, []string{"--help", "two words"}) {
			t.Fatal(ctx.Err(), args)
		}
		if i.Settings.String("GOGO_SCRIPT_LABEL") != "maintenance" {
			t.Fatal("missing settings")
		}
		value, ok := i.Application.Registry.Get("services", "message")
		if !ok || value != "hello" {
			t.Fatal(value, ok)
		}
		_, err := fmt.Fprint(i.Stdout, value)
		return err
	}, []string{"maintenance", "--help", "two words"}, management.Options{Stdout: &stdout, Stderr: &stderr})
	if code != 0 || stdout.String() != "hello" || stderr.Len() != 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
	if want := []string{"register", "open", "ready", "run", "shutdown", "close"}; !reflect.DeepEqual(events, want) {
		t.Fatal(events, want)
	}
	if len(project.Commands) != 0 {
		t.Fatal("caller project mutated")
	}
}

func TestScriptFailureStillClosesResources(t *testing.T) {
	for _, test := range []string{"error", "panic", "canceled"} {
		t.Run(test, func(t *testing.T) {
			cleanScriptEnvironment(t)
			closed := false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			project := Project{Root: t.TempDir(), ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) {
				return []app.Resource{{Name: "custom", Open: func(context.Context) (func(context.Context) error, error) {
					return func(ctx context.Context) error {
						closed = true
						if ctx.Err() != nil {
							t.Fatal("cleanup inherited cancellation")
						}
						return nil
					}, nil
				}}}, nil
			}}
			var stderr bytes.Buffer
			code := runScript(ctx, project, func(context.Context, *management.Invocation, []string) error {
				switch test {
				case "panic":
					panic("private details")
				case "canceled":
					cancel()
					return ctx.Err()
				default:
					return errors.New("private details")
				}
			}, nil, management.Options{Stderr: &stderr})
			if code != 1 || !closed || strings.Contains(stderr.String(), "private details") {
				t.Fatal(code, closed, stderr.String())
			}
		})
	}
}

func TestScriptValidatesRequiredResources(t *testing.T) {
	cleanScriptEnvironment(t)
	opened := false
	project := Project{Root: t.TempDir(), RuntimeResources: []string{"database"}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { opened = true; return nil, nil }}
	var stderr bytes.Buffer
	code := runScript(context.Background(), project, func(context.Context, *management.Invocation, []string) error {
		t.Fatal("ran without configuration")
		return nil
	}, nil, management.Options{Stderr: &stderr})
	if code != 1 || opened || !strings.Contains(stderr.String(), "CONFIG_REQUIRED") {
		t.Fatal(code, opened, stderr.String())
	}
}

func TestCompiledStandaloneScriptUsesProjectConfiguration(t *testing.T) {
	cleanScriptEnvironment(t)
	t.Setenv("GOWORK", "off")
	framework, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(framework, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(framework, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	module := strings.Replace(string(mod), "module github.com/Newton-School/gogo", "module example.com/script-app", 1)
	module += fmt.Sprintf("\nrequire github.com/Newton-School/gogo v0.0.0\nreplace github.com/Newton-School/gogo => %q\n", filepath.ToSlash(framework))
	files := map[string]string{
		"go.mod": module, "go.sum": string(sum),
		".env": "GOGO_HTTP_ADDR=127.0.0.1:9876\n",
		"config/project.go": `package config
import (
 "context"
 "fmt"
 "github.com/Newton-School/gogo"
 "github.com/Newton-School/gogo/core/app"
 "github.com/Newton-School/gogo/core/conf"
)
func Project() gogo.Project {
 return gogo.Project{ResourceFactory:func(conf.Values,[]string)([]app.Resource,error) {
  return []app.Resource{{Name:"test",Open:func(context.Context)(func(context.Context)error,error) {
   fmt.Println("opened")
   return func(context.Context)error { fmt.Println("closed");return nil },nil
  }}},nil
 }}
}
`,
		"scripts/report.go": `package main
import (
 "context"
 "fmt"
 "example.com/script-app/config"
 "github.com/Newton-School/gogo"
 "github.com/Newton-School/gogo/core/management"
)
func main() {
 gogo.Script(config.Project(),func(ctx context.Context,i *management.Invocation,args []string)error {
  _,err:=fmt.Fprintf(i.Stdout,"addr=%s args=%v\n",i.Settings.String("GOGO_HTTP_ADDR"),args)
  return err
 })
}
`,
	}
	for name, data := range files {
		if err = os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Project{Root: root}, []string{"manage", "runscript", "scripts/report.go", "--", "--dry-run"}, management.Options{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if code != 0 || stdout.String() != "opened\naddr=127.0.0.1:9876 args=[--dry-run]\nclosed\n" {
		t.Fatal(code, stdout.String(), stderr.String())
	}
	// Building the script must not rewrite the client's module metadata.
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != files[name] {
			t.Fatalf("%s changed: %v", name, err)
		}
	}
}
