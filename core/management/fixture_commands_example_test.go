package management_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/management"
	"github.com/Newton-School/gogo/core/serialization"
)

func ExampleDumpDataCommand() {
	// In an application, provide a real configured backend and explicit scoped,
	// authorized profiles. This resolver is never called just to register/help.
	resolve := func(context.Context, *app.Registry, conf.Values, string) (*serialization.Fixtures, error) {
		return nil, serialization.ErrConfiguration
	}
	dump := management.DumpDataCommand(resolve)
	load := management.LoadDataCommand(resolve)
	project := management.Project{Commands: []management.Command{dump, load}}
	for _, command := range project.Commands {
		fmt.Println(command.Name, command.RequiredFlags, command.OpenResources)
	}
	// Output:
	// dumpdata [database format] false
	// loaddata [database format] false
}
