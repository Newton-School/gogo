package examples_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
	"github.com/Newton-School/gogo/core/auth"
)

func TestDashboardMountExample(t *testing.T) {
	// docs:begin dashboard-policy
	policy := func(ctx context.Context, action, scope, id string) error {
		p := auth.FromContext(ctx) // Set by verified session/identity middleware.
		if !p.Authenticated || !p.Active || !p.Staff || !p.Superuser {
			return async.ErrDenied
		}
		switch action {
		case "inspect_dashboard", "inspect", "inspect_events":
			return nil
		default:
			return async.ErrDenied
		}
	}
	// docs:end dashboard-policy
	backend := fakes.NewMemory() // Use durable runtime ports in your application.
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Presence: backend, Workflows: backend, Queues: []string{"default"}, Authorize: policy})
	if err != nil {
		t.Fatal(err)
	}
	catalog := async.DashboardCatalog(backend)
	mux, err := mountDashboardExample(client, catalog, policy)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/async/", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Authenticated: true, Active: true, Staff: true, Superuser: true}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func mountDashboardExample(client *async.Client, catalog async.DashboardCatalog, policy func(context.Context, string, string, string) error) (http.Handler, error) {
	mux := http.NewServeMux()
	// docs:begin dashboard-mount
	dashboard, err := async.NewDashboard(async.DashboardConfig{
		Client: client, Catalog: catalog, BasePath: "/async/",
		Title: "Shop Async", PageSize: 25, Timeout: 5 * time.Second,
		Authorize: func(r *http.Request) error {
			return policy(r.Context(), "inspect_dashboard", "", "overview")
		},
	})
	if err != nil {
		return nil, err
	}
	mux.Handle("/async/", dashboard)
	// docs:end dashboard-mount
	return mux, nil
}
