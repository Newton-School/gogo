package config

import (
	"context"
	"net/http"

	"github.com/Newton-School/gogo/async"
	asyncredis "github.com/Newton-School/gogo/async/redis"
	"github.com/Newton-School/gogo/core/auth"
)

// The showcase inventory is operator-wide and requires an active superuser.
// Ordinary staff membership does not grant access to background task metadata.
func dashboardAccess(ctx context.Context) error {
	p := auth.FromContext(ctx)
	if !p.Authenticated || !p.Active || !p.Staff || !p.Superuser {
		return async.ErrDenied
	}
	return ctx.Err()
}

func (c *Connections) Dashboard() (http.Handler, error) {
	r, err := c.taskRuntime()
	if err != nil {
		return nil, err
	}
	client, err := async.NewClient(async.ClientConfig{Registry: r.tasks.Registry, Broker: r.broker, Results: r.results, Workflows: r.workflows, Presence: r.workers, Queues: []string{"showcase"}, Authorize: func(ctx context.Context, action, scope, id string) error {
		switch action {
		case "inspect_dashboard", "inspect", "inspect_events":
			return dashboardAccess(ctx)
		default:
			return async.ErrDenied
		}
	}})
	if err != nil {
		return nil, err
	}
	dashboard, err := async.NewDashboard(async.DashboardConfig{Client: client, BasePath: "/async/", Title: "Showcase Async", Events: r.events, Authorize: func(r *http.Request) error { return dashboardAccess(r.Context()) }, Catalog: &asyncredis.DashboardCatalog{Results: r.results, Workflows: r.workflows, Workers: r.workers, Schedules: r.schedules, Beats: r.beats}})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.FromContext(r.Context()).Authenticated {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/admin/login/?next=/async/", http.StatusSeeOther)
			return
		}
		dashboard.ServeHTTP(w, r)
	}), nil
}
