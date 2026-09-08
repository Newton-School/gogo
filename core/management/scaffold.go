package management

import (
	"context"
	"errors"
	"flag"
	"github.com/Newton-School/gogo/internal/codegen"
)

func scaffoldCommands() []Command {
	return []Command{
		{Name: "generate", Help: "Generate typed model references and registrations", Validate: noArgs, Configure: func(flags *flag.FlagSet) Runner {
			check := flags.Bool("check", false, "verify descriptors without writing source")
			return func(ctx context.Context, invocation *Invocation, _ []string) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if *check {
					return codegen.CheckGenerated(invocation.Project.Root)
				}
				return codegen.Generate(invocation.Project.Root)
			}
		}},
		{Name: "startapp", Help: "Create and explicitly register an app", Validate: func(args []string) error {
			if len(args) != 1 {
				return errors.New("one app label required")
			}
			return nil
		}, Configure: fixed(func(_ context.Context, i *Invocation, args []string) error {
			return codegen.StartApp(i.Project.Root, args[0])
		})},
		{Name: "startproject", Help: "Create a structured Go project", Validate: func(args []string) error {
			if len(args) != 1 {
				return errors.New("one project directory required")
			}
			return nil
		}, Configure: func(f *flag.FlagSet) Runner {
			module := f.String("module", "", "new Go module path (required)")
			version := f.String("framework-version", "v0.0.0", "framework module version")
			return func(_ context.Context, _ *Invocation, args []string) error {
				return codegen.StartProject(args[0], codegen.ProjectOptions{Module: *module, Version: *version})
			}
		}},
	}
}
