package main

import (
	"context"
	"github.com/Newton-School/gogo/core/management"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(management.Run(ctx, management.Project{Name: "gogo"}, os.Args, management.Options{}))
}
