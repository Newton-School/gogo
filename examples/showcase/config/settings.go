package config

import (
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/management"
)

func Settings() conf.Schema {
	schema := conf.CoreSchema()
	for i := range schema {
		if schema[i].Name == "GOGO_SESSION_BACKEND" {
			schema[i].Default = "redis"
			schema[i].Choices = []string{"redis"}
		}
	}
	return append(schema,
		conf.Definition{Name: "GOGO_SHOWCASE_SCHEMA", Group: "Showcase", Default: "public"},
		conf.Definition{Name: "GOGO_SHOWCASE_ADMIN_IDENTIFIER", Group: "Bootstrap", RequiredFor: []string{"bootstrap"}},
		conf.Definition{Name: "GOGO_SHOWCASE_ADMIN_PASSWORD", Group: "Bootstrap", Sensitive: true, RequiredFor: []string{"bootstrap"}},
	)
}

func Project() management.Project {
	connections := &Connections{}
	commands := management.MigrationCommands(func() db.Backend { return connections.Database }, func() db.SchemaEditor { return connections.Database.SchemaEditor() })
	commands = append(commands, connections.Commands()...)
	commands = append(commands, connections.AsyncCommands()...)
	return management.Project{
		Name: "Gogo showcase", Root: ".", Schema: Settings(), Apps: InstalledApps(),
		RuntimeResources: []string{"database", "redis", "sessions", "auth", "signing"},
		ResourceFactory:  connections.Resources, Handler: connections.Handler, Commands: commands,
	}
}
