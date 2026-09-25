package main

import (
	"context"
	"errors"
	"fmt"

	"example.com/gogo-showcase/apps/catalog"
	"example.com/gogo-showcase/config"
	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/core/management"
	"github.com/Newton-School/gogo/core/orm"
)

// docs:begin runscript-database
func main() {
	connections := &config.Connections{}
	project := config.Project()
	project.RuntimeResources = []string{"database"}
	project.ResourceFactory = connections.Resources
	gogo.Script(project, func(ctx context.Context, i *management.Invocation, args []string) error {
		if len(args) != 0 {
			return errors.New("catalog report takes no arguments")
		}
		count, err := orm.For(connections.Store, func() *catalog.Product {
			return &catalog.Product{}
		}).Count(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(i.Stdout, "Products: %d\n", count)
		return err
	})
}

// docs:end runscript-database
