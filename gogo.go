// Package gogo is the entry point for structured Go backend projects. Optional
// Admin, Async and connector modules are imported explicitly by clients.
package gogo

import (
	"context"
	"github.com/Newton-School/gogo/core/management"
	"os"
	"os/signal"
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
