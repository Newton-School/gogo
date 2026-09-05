package management

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"testing"
)

func TestInvalidInvocationDoesNotOpenResources(t *testing.T) {
	opened := false
	p := Project{Root: t.TempDir(), RuntimeResources: []string{"database"}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { opened = true; return nil, nil }}
	var output bytes.Buffer
	code := Run(context.Background(), p, []string{"manage", "serve", "--unknown"}, Options{Stderr: &output, Stdout: &output})
	if code != 2 || opened {
		t.Fatal(code, opened)
	}
}
func TestAppCommandResolutionBeforeReady(t *testing.T) {
	ready := false
	called := false
	p := Project{Root: t.TempDir(), Apps: []app.Config{{Name: "catalog", Label: "catalog", Ready: func(context.Context, *app.Registry) error { ready = true; return nil }, Register: func(r *app.Registry) error {
		return RegisterCommand(r, Command{Name: "hello", Configure: func(*flag.FlagSet) Runner {
			return func(context.Context, *Invocation, []string) error { called = true; return nil }
		}})
	}}}}
	if err := Call(context.Background(), p, []string{"hello"}, Options{}); err != nil || !called || ready {
		t.Fatal(err, called, ready)
	}
}
func TestSafeCLIErrorAndConditionalRequirements(t *testing.T) {
	var output bytes.Buffer
	p := Project{Root: t.TempDir(), RuntimeResources: []string{"database"}, Commands: []Command{{Name: "fail", Configure: func(*flag.FlagSet) Runner {
		return func(context.Context, *Invocation, []string) error { return errors.New("private password") }
	}}}}
	if code := Run(context.Background(), p, []string{"manage", "fail"}, Options{Stdout: &output, Stderr: &output}); code != 1 || bytes.Contains(output.Bytes(), []byte("private password")) {
		t.Fatal(code, output.String())
	}
	if err := Call(context.Background(), p, []string{"check"}, Options{Stdout: &output, Stderr: &output}); err == nil {
		t.Fatal("check ignored missing DB config")
	}
	if err := Call(context.Background(), p, []string{"version"}, Options{Stdout: &output, Stderr: &output}); err != nil {
		t.Fatal(err)
	}
}

func TestInterspersedFlags(t *testing.T) {
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	module := set.String("module", "", "module")
	dry := set.Bool("dry-run", false, "dry")
	if err := parseFlags(set, []string{"storefront", "--module", "example.com/storefront", "--dry-run"}); err != nil || *module != "example.com/storefront" || !*dry || len(set.Args()) != 1 || set.Args()[0] != "storefront" {
		t.Fatal(err, *module, *dry, set.Args())
	}
}
