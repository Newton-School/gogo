package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/internal/version"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
)

// Root dispatches built-in CLI commands.
type Root struct {
	registry *Registry
}

// NewRoot creates the root CLI with all planned built-in commands.
func NewRoot() *Root {
	return NewRootWithOptions(RootOptions{})
}

// RootOptions configures project-specific command integrations.
type RootOptions struct {
	RunserverStarter  ServerStarter
	AuthStore         authUserStore
	QueueRuntime      *QueueRuntime
	FixtureStore      FixtureStore
	ProjectChecks     []checks.Check
	ProjectMigrations []migrations.Migration
	ProjectModels     []models.Metadata
}

// NewRootWithOptions creates the root CLI with project-specific integrations.
func NewRootWithOptions(options RootOptions) *Root {
	registry := NewRegistry()
	root := &Root{registry: registry}

	for _, command := range plannedCommands(root, options) {
		root.mustRegister(command)
	}

	return root
}

// Execute runs a root command.
func (r *Root) Execute(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		args = []string{"help"}
	}
	if len(args) == 1 {
		switch args[0] {
		case "--help", "-h":
			args = []string{"help"}
		case "--version", "-version":
			args = []string{"version"}
		}
	}

	command, err := r.registry.Get(args[0])
	if err != nil {
		return err
	}

	runArgs := []string(nil)
	if len(args) > 1 {
		runArgs = args[1:]
	}

	runner, ok := command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	})
	if ok {
		return runner.runWithIO(ctx, runArgs, stdout, stderr)
	}

	return command.Run(ctx, runArgs)
}

func (r *Root) mustRegister(command Command) {
	if err := r.registry.Register(command); err != nil {
		panic(err)
	}
}

// Register adds a project-provided command to the root registry.
func (r *Root) Register(command Command) error {
	return r.registry.Register(command)
}

type helpCommand struct {
	root *Root
}

func (c helpCommand) Name() string {
	return "help"
}

func (c helpCommand) Summary() string {
	return "Show this help message"
}

func (c helpCommand) Run(context.Context, []string) error {
	return nil
}

func (c helpCommand) runWithIO(_ context.Context, _ []string, stdout, _ io.Writer) error {
	_, err := fmt.Fprintln(stdout, "Usage: gogo <command> [args]\n\nCommands:")
	if err != nil {
		return fmt.Errorf("%w: write help: %v", ErrCommandFailed, err)
	}

	for _, command := range c.root.registry.Commands() {
		if _, err := fmt.Fprintf(stdout, "  %-16s %s\n", command.Name(), command.Summary()); err != nil {
			return fmt.Errorf("%w: write help: %v", ErrCommandFailed, err)
		}
	}

	return nil
}

type versionCommand struct{}

func (c versionCommand) Name() string {
	return "version"
}

func (c versionCommand) Summary() string {
	return "Show version metadata"
}

func (c versionCommand) Run(context.Context, []string) error {
	return nil
}

func (c versionCommand) runWithIO(_ context.Context, _ []string, stdout, _ io.Writer) error {
	if _, err := fmt.Fprintln(stdout, version.Info()); err != nil {
		return fmt.Errorf("%w: write version: %v", ErrCommandFailed, err)
	}

	return nil
}

func plannedCommands(root *Root, options RootOptions) []Command {
	fixtureStore := options.FixtureStore
	if fixtureStore == nil {
		fixtureStore = NewMemoryFixtureStore()
	}
	authStore := defaultAuthStore
	if options.AuthStore != nil {
		authStore = options.AuthStore
	}
	queueRuntime := defaultQueueRuntime
	if options.QueueRuntime != nil {
		queueRuntime = options.QueueRuntime
	}
	return []Command{
		helpCommand{root: root},
		versionCommand{},
		NewCheckCommandWithProject(options.ProjectModels, options.ProjectMigrations, options.ProjectChecks...),
		NewRunserverCommand(options.RunserverStarter),
		NewStartprojectCommand(),
		NewStartappCommand(),
		NewMakemigrationsCommandWithProject(options.ProjectModels, options.ProjectMigrations),
		NewMigrateCommandWithMigrations(options.ProjectMigrations),
		NewShowmigrationsCommandWithMigrations(options.ProjectMigrations),
		NewSQLMigrateCommandWithMigrations(options.ProjectMigrations),
		NewSquashmigrationsCommandWithMigrations(options.ProjectMigrations),
		NewOptimizeMigrationCommandWithMigrations(options.ProjectMigrations),
		NewInspectDBCommand(),
		NewDiffSchemaCommand(options.ProjectModels),
		NewCreateSuperuserCommand(authStore),
		NewChangePasswordCommand(authStore),
		NewCollectstaticCommand(nil),
		NewShellCommand(nil),
		NewDBShellCommand(nil),
		NewTestCommand(nil),
		NewWorkerCommand(queueRuntime),
		NewBeatCommand(queueRuntime),
		NewInspectCommand(queueRuntime),
		NewQueuesCommand(queueRuntime),
		NewDumpdataCommand(fixtureStore),
		NewLoaddataCommand(fixtureStore),
	}
}
