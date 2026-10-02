package main

import (
	"example.com/gogo-services/apps/catalog"
	"example.com/gogo-services/config/settings"
	"example.com/gogo-services/internal/database"
	"example.com/gogo-services/internal/httpserver"
	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"net/http"
)

func Project() gogo.Project {
	connection := &database.Connection{}
	return gogo.Project{Name: "api", MainPackage: "./services/api", Schema: settings.Schema(), Apps: []app.Config{catalog.App()}, RuntimeResources: []string{"database"}, ResourceFactory: connection.Resources,
		Handler: func(_ *app.Registry, settings conf.Values) (http.Handler, error) {
			return httpserver.Handler(connection, settings)
		},
	}
}
