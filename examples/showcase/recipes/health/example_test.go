package health_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/health"
	"github.com/Newton-School/gogo/core/urls"
)

func Example_healthNewHandler() {
	application, err := app.Bootstrap(context.Background(), nil, nil, nil)
	if err != nil {
		panic(err)
	}
	defer application.Close(context.Background())
	checker, err := health.New(health.Config{
		Role:  "web",
		State: health.FromApplication(application),
		Dependencies: []health.Dependency{{
			ID: "database", Roles: []string{"web"},
			// Use the application's database.Ping bound method in real wiring.
			Probe: func(context.Context) error { return errors.New("offline") },
		}},
	})
	if err != nil {
		panic(err)
	}
	var routes []urls.Route
	for _, kind := range []health.Kind{health.KindLive, health.KindStartup, health.KindReady} {
		handler, err := health.NewHandler(checker, health.HTTPOptions{Kind: kind})
		if err != nil {
			panic(err)
		}
		routes = append(routes, urls.Path("/health/"+string(kind), handler, "health-"+string(kind)))
	}
	router, err := urls.New(routes...)
	if err != nil {
		panic(err)
	}
	for _, path := range []string{"/health/live", "/health/startup", "/health/ready"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		fmt.Println(path, response.Code, response.Body.String())
	}
	// Output:
	// /health/live 200 {"status":"live"}
	// /health/startup 200 {"status":"started"}
	// /health/ready 503 {"status":"not_ready"}
}
