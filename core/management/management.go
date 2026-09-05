// Package management runs the same explicit project through web and CLI roles.
package management

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/checks"
	"github.com/Newton-School/gogo/core/conf"
	ghttp "github.com/Newton-School/gogo/core/http"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const Version = "0.0.0-dev"

type Invocation struct {
	Project        *Project
	Application    *app.Application
	Settings       conf.Values
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}
type Runner func(context.Context, *Invocation, []string) error
type Command struct {
	Name, Help    string
	Resources     []string
	OpenResources bool
	Configure     func(*flag.FlagSet) Runner
	Validate      func([]string) error
}
type Project struct {
	Name, Root       string
	Schema           conf.Schema
	Environment      map[string]string
	Apps             []app.Config
	Freeze           func(*app.Registry) error
	RuntimeResources []string
	ResourceFactory  func(conf.Values, []string) ([]app.Resource, error)
	Handler          func(*app.Registry, conf.Values) (http.Handler, error)
	Commands         []Command
	Checks           *checks.Registry
}
type Options struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

func (options Options) defaults() Options {
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	return options
}

type CommandError struct {
	Code    int
	Message string
	Cause   error
}

func (e *CommandError) Error() string { return e.Message }
func (e *CommandError) Unwrap() error { return e.Cause }

// Call is the programmatic entry point. Args start at the command name, unlike
// Run's os.Args-compatible input. Flag parsing always precedes opening resources.
func Call(ctx context.Context, project Project, args []string, options Options) error {
	options = options.defaults()
	if project.Root == "" {
		project.Root = "."
	}
	if len(project.Schema) == 0 {
		project.Schema = conf.CoreSchema()
	}
	application, err := app.Prepare(project.Apps, project.Freeze)
	if err != nil {
		return err
	}
	commands := map[string]Command{}
	register := func(command Command) error {
		if command.Name == "" || command.Configure == nil {
			return errors.New("invalid management command")
		}
		if _, ok := commands[command.Name]; ok {
			return fmt.Errorf("duplicate management command: %s", command.Name)
		}
		commands[command.Name] = command
		return nil
	}
	for _, command := range builtins(&project) {
		if err = register(command); err != nil {
			return err
		}
	}
	for _, command := range project.Commands {
		if err = register(command); err != nil {
			return err
		}
	}
	for _, name := range application.Registry.Names("commands") {
		value, _ := application.Registry.Get("commands", name)
		command, ok := value.(Command)
		if !ok {
			return errors.New("invalid registered command")
		}
		if err = register(command); err != nil {
			return err
		}
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		names := make([]string, 0, len(commands))
		for name := range commands {
			names = append(names, name)
		}
		slices.Sort(names)
		_, err = fmt.Fprintln(options.Stdout, "Gogo management commands:")
		if err != nil {
			return err
		}
		for _, name := range names {
			if _, err = fmt.Fprintf(options.Stdout, "  %-18s %s\n", name, commands[name].Help); err != nil {
				return err
			}
		}
		return nil
	}
	command, ok := commands[args[0]]
	if !ok {
		return &CommandError{2, "Unknown command: " + args[0], nil}
	}
	flags := flag.NewFlagSet(command.Name, flag.ContinueOnError)
	flags.SetOutput(options.Stderr)
	runner := command.Configure(flags)
	if runner == nil {
		return errors.New("management command runner required")
	}
	if err = parseFlags(flags, args[1:]); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return &CommandError{2, "Invalid command arguments", err}
	}
	if command.Validate != nil {
		if err = command.Validate(flags.Args()); err != nil {
			return &CommandError{2, "Invalid command arguments", err}
		}
	}
	resources := slices.Clone(command.Resources)
	if slices.Contains(resources, "runtime") {
		resources = slices.DeleteFunc(resources, func(s string) bool { return s == "runtime" })
		resources = append(resources, project.RuntimeResources...)
	}
	env := map[string]string{}
	file, e := os.Open(filepath.Join(project.Root, ".env"))
	if e == nil {
		env, e = conf.ReadEnv(file)
		_ = file.Close()
		if e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return errors.New("cannot read project environment")
	}
	env = conf.Merge(env, project.Environment, conf.Environment(os.Environ()))
	settings, err := project.Schema.Load(env, resources...)
	if err != nil {
		return err
	}
	invocation := &Invocation{&project, application, settings, options.Stdin, options.Stdout, options.Stderr}
	if command.OpenResources {
		var selected []app.Resource
		if project.ResourceFactory != nil {
			selected, err = project.ResourceFactory(settings, resources)
			if err != nil {
				return err
			}
		} else if len(resources) > 0 {
			return errors.New("selected resources require a resource factory")
		}
		if err = application.Start(ctx, selected); err != nil {
			return err
		}
	}
	err = runner(ctx, invocation, flags.Args())
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), settings.Duration("GOGO_SHUTDOWN_GRACE"))
	defer cancel()
	return errors.Join(err, application.Close(cleanup))
}
func Run(ctx context.Context, project Project, args []string, options Options) int {
	options = options.defaults()
	if len(args) > 0 {
		args = args[1:]
	}
	err := Call(ctx, project, args, options)
	if err == nil {
		return 0
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		_, _ = fmt.Fprintln(options.Stderr, commandErr.Message)
		return commandErr.Code
	}
	var configErr *conf.ConfigError
	if errors.As(err, &configErr) {
		_, _ = fmt.Fprintln(options.Stderr, configErr.Error())
	} else {
		_, _ = fmt.Fprintln(options.Stderr, "Command failed; operation did not complete")
	}
	return 1
}
func noArgs(args []string) error {
	if len(args) > 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}
func fixed(run Runner) func(*flag.FlagSet) Runner { return func(*flag.FlagSet) Runner { return run } }
func builtins(project *Project) []Command {
	commands := append(scaffoldCommands(), []Command{
		{Name: "version", Help: "Print framework version", Validate: noArgs, Configure: fixed(func(_ context.Context, i *Invocation, _ []string) error {
			_, e := fmt.Fprintln(i.Stdout, Version)
			return e
		})},
		{Name: "diffsettings", Help: "Print resolved settings with secrets redacted", Validate: noArgs, Configure: fixed(func(_ context.Context, i *Invocation, _ []string) error {
			encoder := json.NewEncoder(i.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(i.Settings)
		})},
		{Name: "check", Help: "Run read-only system and deployment checks", Resources: []string{"runtime"}, Validate: noArgs, Configure: func(f *flag.FlagSet) Runner {
			deploy := f.Bool("deploy", false, "run deployment checks")
			probe := f.Bool("probe", false, "explicitly probe selected backends")
			tags := f.String("tag", "", "comma-separated check tags")
			return func(ctx context.Context, i *Invocation, _ []string) error {
				registry := i.Project.Checks
				if registry == nil {
					registry = &checks.Registry{}
				}
				findings, err := registry.Run(ctx, checks.Options{Deploy: *deploy, Probe: *probe, Tags: split(*tags)})
				if *deploy {
					if i.Settings.Bool("GOGO_DEBUG") {
						findings = append(findings, checks.Finding{ID: "security.E001", Severity: checks.Error, Message: "Disable DEBUG in production", Security: true})
						err = errors.New("deployment checks failed")
					}
					if len(i.Settings.List("GOGO_ALLOWED_HOSTS")) == 0 {
						findings = append(findings, checks.Finding{ID: "security.E002", Severity: checks.Error, Message: "Configure production allowed hosts", Security: true})
						err = errors.New("deployment checks failed")
					}
				}
				if e := json.NewEncoder(i.Stdout).Encode(findings); e != nil {
					return e
				}
				return err
			}
		}},
		{Name: "build", Help: "Validate runtime configuration and compile manage", Resources: []string{"runtime"}, Validate: noArgs, Configure: func(f *flag.FlagSet) Runner {
			output := f.String("o", "bin/manage", "output path")
			return func(ctx context.Context, i *Invocation, _ []string) error {
				cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", *output, "manage.go")
				cmd.Dir = i.Project.Root
				cmd.Stdin = i.Stdin
				cmd.Stdout = i.Stdout
				cmd.Stderr = i.Stderr
				return cmd.Run()
			}
		}},
		{Name: "test", Help: "Run project Go tests", Configure: func(f *flag.FlagSet) Runner {
			race := f.Bool("race", false, "enable race detector")
			return func(ctx context.Context, i *Invocation, args []string) error {
				argv := []string{"test"}
				if *race {
					argv = append(argv, "-race")
				}
				if len(args) == 0 {
					args = []string{"./..."}
				}
				argv = append(argv, args...)
				cmd := exec.CommandContext(ctx, "go", argv...)
				cmd.Dir = i.Project.Root
				cmd.Stdin = i.Stdin
				cmd.Stdout = i.Stdout
				cmd.Stderr = i.Stderr
				return cmd.Run()
			}
		}},
	}...)
	for _, name := range []string{"serve", "runserver"} {
		commands = append(commands, Command{Name: name, Help: "Serve the explicitly configured project", Resources: []string{"runtime"}, OpenResources: true, Validate: noArgs, Configure: func(f *flag.FlagSet) Runner {
			address := f.String("addr", "", "listen address override")
			return func(ctx context.Context, i *Invocation, _ []string) error {
				if i.Project.Handler == nil {
					return errors.New("project HTTP handler required")
				}
				handler, err := i.Project.Handler(i.Application.Registry, i.Settings)
				if err != nil {
					return err
				}
				addr := *address
				if addr == "" {
					addr = i.Settings.String("GOGO_HTTP_ADDR")
				}
				server, err := ghttp.NewServer(ghttp.ServerConfig{Address: addr, Handler: handler, ReadHeaderTimeout: i.Settings.Duration("GOGO_HTTP_READ_HEADER_TIMEOUT"), IdleTimeout: i.Settings.Duration("GOGO_HTTP_IDLE_TIMEOUT"), ShutdownGrace: i.Settings.Duration("GOGO_SHUTDOWN_GRACE")})
				if err != nil {
					return err
				}
				return server.ListenAndServe(ctx)
			}
		}})
	}
	return commands
}
func split(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

// RegisterCommand contributes an app command during App.Register, not init().
func RegisterCommand(registry *app.Registry, command Command) error {
	return registry.Register("commands", command.Name, command)
}
