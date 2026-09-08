package management

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
)

func TestRequiredFlagsPrecedeResourcesAndPreserveHelp(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, test := range []struct {
		name     string
		required []string
		args     []string
		code     int
		run      bool
	}{
		{"missing", []string{"value"}, nil, 2, false},
		{"explicit", []string{"value"}, []string{"--value=selected"}, 0, true},
		{"explicit_false", []string{"enabled"}, []string{"--enabled=false"}, 0, true},
		{"unknown_declaration", []string{"unknown"}, []string{"--value=selected"}, 2, false},
		{"duplicate", []string{"value", "value"}, []string{"--value=selected"}, 2, false},
		{"invalid_name", []string{"bad=name"}, nil, 2, false},
		{"long_name", []string{strings.Repeat("a", 129)}, nil, 2, false},
		{"many", make([]string, 129), nil, 2, false},
		{"help_missing", []string{"value"}, []string{"--help"}, 0, false},
		{"help_unknown", []string{"unknown"}, []string{"--help"}, 0, false},
		{"legacy", nil, nil, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			opened, ran := false, false
			command := Command{Name: "required", RequiredFlags: test.required, OpenResources: true, Configure: func(f *flag.FlagSet) Runner {
				f.String("value", "default-does-not-count", "value")
				f.Bool("enabled", true, "enabled")
				return func(context.Context, *Invocation, []string) error { ran = true; return nil }
			}}
			project := Project{Root: t.TempDir(), Commands: []Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { opened = true; return nil, nil }}
			err := Call(context.Background(), project, append([]string{"required"}, test.args...), Options{Stdout: io.Discard, Stderr: io.Discard})
			if test.code == 0 && err != nil || test.code != 0 && (err == nil || err.(*CommandError).Code != test.code) || opened != test.run || ran != test.run {
				t.Fatalf("error=%v opened=%v ran=%v", err, opened, ran)
			}
		})
	}
}

func TestRequiredFlagsCapturedBeforeApplicationAndConfigureCallbacks(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, phase := range []string{"freeze", "configure", "flag_value", "registry_configure"} {
		t.Run(phase, func(t *testing.T) {
			names := []string{"original"}
			called := false
			command := Command{Name: "required", RequiredFlags: names, Configure: func(f *flag.FlagSet) Runner {
				if phase == "configure" || phase == "registry_configure" {
					names[0] = "replacement"
				}
				f.Func("original", "original", func(string) error {
					if phase == "flag_value" {
						names[0] = "replacement"
					}
					return nil
				})
				f.String("replacement", "", "replacement")
				return func(context.Context, *Invocation, []string) error { called = true; return nil }
			}}
			project := Project{Root: t.TempDir(), Commands: []Command{command}}
			if phase == "freeze" {
				project.Freeze = func(*app.Registry) error { names[0] = "replacement"; return nil }
			}
			if phase == "registry_configure" {
				project.Commands = nil
				project.Apps = []app.Config{{Name: "fixture", Label: "fixture", Register: func(r *app.Registry) error { return RegisterCommand(r, command) }}}
			}
			if err := Call(context.Background(), project, []string{"required", "--original=value"}, Options{Stdout: io.Discard, Stderr: io.Discard}); err != nil || !called {
				t.Fatal(err, called)
			}
		})
	}
}

func TestRequiredFlagsArePerCallAndConcurrent(t *testing.T) {
	isolateCommandEnvironment(t)
	var called atomic.Int32
	command := Command{Name: "required", RequiredFlags: []string{"value"}, Configure: func(f *flag.FlagSet) Runner {
		value := f.String("value", "", "value")
		return func(context.Context, *Invocation, []string) error {
			if *value != "selected" {
				return errors.New("wrong invocation value")
			}
			called.Add(1)
			return nil
		}
	}}
	project := Project{Root: t.TempDir(), Commands: []Command{command}}
	var group sync.WaitGroup
	for index := range 24 {
		group.Add(1)
		go func() {
			defer group.Done()
			args := []string{"required"}
			if index%2 == 0 {
				args = append(args, "--value=selected")
			}
			err := Call(context.Background(), project, args, Options{Stdout: io.Discard, Stderr: io.Discard})
			if index%2 == 0 && err != nil || index%2 != 0 && err == nil {
				t.Errorf("invocation %d: %v", index, err)
			}
		}()
	}
	group.Wait()
	if called.Load() != 12 {
		t.Fatal(called.Load())
	}
}

func TestRequiredFlagsCannotBeSatisfiedByProgrammaticSet(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, phase := range []string{"configure", "other_flag", "value_token", "after_boundary"} {
		t.Run(phase, func(t *testing.T) {
			opened := false
			command := Command{Name: "required", RequiredFlags: []string{"database"}, OpenResources: true, Configure: func(f *flag.FlagSet) Runner {
				f.String("database", "", "database")
				f.String("value", "", "ordinary value")
				f.Func("other", "other", func(string) error { return f.Set("database", "programmatic") })
				if phase == "configure" {
					_ = f.Set("database", "programmatic")
				}
				return func(context.Context, *Invocation, []string) error { t.Error("invalid command ran"); return nil }
			}}
			args := []string{"required"}
			switch phase {
			case "other_flag":
				args = append(args, "--other=value")
			case "value_token":
				args = append(args, "--value", "--database")
			case "after_boundary":
				args = append(args, "--", "--database=value")
			}
			project := Project{Root: t.TempDir(), Commands: []Command{command}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { opened = true; return nil, nil }}
			err := Call(context.Background(), project, args, Options{Stdout: io.Discard, Stderr: io.Discard})
			var commandErr *CommandError
			if !errors.As(err, &commandErr) || commandErr.Code != 2 || opened {
				t.Fatal(err, opened)
			}
		})
	}
}
