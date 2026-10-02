package config

import (
	"context"
	"errors"
	"example.com/gogo-services/apps/catalog"
	"example.com/gogo-services/config/settings"
	"example.com/gogo-services/internal/database"
	"flag"
	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/management"
)

// Project is the complete migration/maintenance registry, not an HTTP server.
func Project() gogo.Project {
	connection := &database.Connection{}
	commands := management.MigrationCommands(func() db.Backend { return connection.Backend }, func() db.SchemaEditor { return connection.Backend.SchemaEditor() })
	commands = append(commands, management.Command{
		Name: "seed", Help: "Insert the public demonstration products if missing",
		Resources: []string{"database"}, OpenResources: true,
		Validate: func(args []string) error {
			if len(args) != 0 {
				return errors.New("seed takes no arguments")
			}
			return nil
		},
		Configure: func(*flag.FlagSet) management.Runner {
			return func(ctx context.Context, _ *management.Invocation, _ []string) error {
				return catalog.Seed(ctx, connection.Store)
			}
		},
	})
	return gogo.Project{Name: "Services example", Schema: settings.Schema(), Apps: InstalledApps(), ResourceFactory: connection.Resources, Commands: commands}
}
