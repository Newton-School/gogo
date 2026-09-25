package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/core/management"
)

// This compile-checked example intentionally has no Output directive: Script
// owns os.Exit. The root package's compiled-consumer test executes this API.
func ExampleScript() {
	// docs:begin script-entry
	project := gogo.Project{Name: "maintenance"} // Use config.Project() in a client.
	gogo.Script(project, func(ctx context.Context, i *management.Invocation, args []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := fmt.Fprintf(i.Stdout, "Arguments: %q\n", args)
		return err
	})
	// docs:end script-entry
}
