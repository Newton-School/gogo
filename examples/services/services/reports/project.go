package main

import (
	"example.com/gogo-services/apps/catalog"
	"example.com/gogo-services/config/settings"
	"example.com/gogo-services/internal/database"
	"example.com/gogo-services/internal/httpserver"
	"example.com/gogo-services/internal/taskqueue"
	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/management"
	"net/http"
)

func Project() gogo.Project {
	database := &database.Connection{}
	queue := &taskqueue.Connection{}
	return gogo.Project{Name: "reports", MainPackage: "./services/reports", Schema: settings.Schema(), Apps: []app.Config{catalog.App()}, RuntimeResources: []string{"database"},
		ResourceFactory: func(settings conf.Values, names []string) ([]app.Resource, error) {
			resources, err := database.Resources(settings, names)
			if err != nil {
				return nil, err
			}
			queues, err := queue.Resources(settings, names)
			if err != nil {
				return nil, err
			}
			return append(resources, queues...), nil
		},
		Handler: func(_ *app.Registry, settings conf.Values) (http.Handler, error) {
			return httpserver.Handler(database, settings)
		},
		Commands: []management.Command{reportCommand(database, queue)},
	}
}
