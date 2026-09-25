// Package gogo is the entry point for structured Go backend projects. Optional
// Admin, Async and connector modules are imported explicitly by clients.
package gogo

import (
	"context"
	"flag"
	"github.com/Newton-School/gogo/core/management"
	"os"
	"os/signal"
	"slices"
	"syscall"
)

const Version = management.Version

type Project = management.Project

func Run(ctx context.Context, project Project, args []string, options management.Options) int {
	return management.Run(ctx, project, args, options)
}

// Main owns process signals and exit status. Use Run or management.Call in tests.
func Main(project Project) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(Run(ctx, project, os.Args, management.Options{}))
}

// Script is the main entry point for a trusted standalone Go script. It loads
// project settings, opens RuntimeResources, runs app Ready hooks, invokes run,
// and closes resources. It never starts the HTTP handler or worker commands.
// Arguments are passed verbatim to run. Select only needed RuntimeResources on
// project before calling Script. Like Main, Script owns signals and os.Exit;
// return errors from run rather than calling os.Exit so cleanup can finish.
func Script(project Project, run management.Runner) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runScript(ctx, project, run, os.Args, management.Options{}))
}

func runScript(ctx context.Context, project Project, run management.Runner, args []string, options management.Options) int {
	// Private command registration reuses normal validation and lifecycle, with
	// no new public package and no registration in ordinary server processes.
	project.Commands = append(slices.Clone(project.Commands), management.Command{
		Name: "__gogo_script__", Help: "Run this script callback",
		Resources: []string{"runtime"}, OpenResources: true,
		Configure: func(*flag.FlagSet) management.Runner { return run },
	})
	forward := []string{"script", "__gogo_script__", "--"}
	if len(args) > 0 {
		forward = append(forward, args[1:]...)
	}
	return Run(ctx, project, forward, options)
}
