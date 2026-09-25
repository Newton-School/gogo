package main

import (
	"context"
	"fmt"

	"example.com/gogo-showcase/config"
	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/core/management"
)

// docs:begin runscript-bootstrap
func main() {
	project := config.Project()
	// This script only inspects registrations, so it needs no connections.
	project.RuntimeResources = nil
	gogo.Script(project, func(ctx context.Context, i *management.Invocation, args []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := fmt.Fprintf(i.Stdout, "Registered models: %d\nArguments: %q\n",
			len(i.Application.Registry.Names("models")), args)
		return err
	})
}

// docs:end runscript-bootstrap
