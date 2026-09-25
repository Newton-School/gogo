// This loopback-only preview uses disposable in-memory fixtures, never a
// project's settings, credentials or Redis. Production wiring lives in config.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := preview()
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: "127.0.0.1:5555", Handler: h, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(closeCtx)
	}()
	log.Print("Fixture-only dashboard: http://127.0.0.1:5555/async/ (no live application data)")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func preview() (http.Handler, error) {
	ctx := context.Background()
	m := fakes.NewMemory()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "reports.export", 1, func(_ context.Context, _ async.TaskContext, input string) (string, error) {
		if input == "fail" {
			return "", errors.New("demonstration failure")
		}
		return "ready", nil
	}, async.TaskOptions{})
	if err != nil {
		return nil, err
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: m, Results: m, Workflows: m, Schedules: m, Presence: m, Queues: []string{"default"}, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		return nil, err
	}
	worker := &async.Worker{Registry: registry, Client: client, Broker: m, Results: m, ID: "reports-worker", Queues: []string{"default"}, Concurrency: 2, Presence: m}
	for _, input := range []string{"ready", "fail"} {
		if _, err := task.Delay(ctx, client, input); err != nil {
			return nil, err
		}
		if err := worker.RunOnce(ctx); err != nil {
			return nil, err
		}
	}
	if _, err := task.Delay(ctx, client, "waiting"); err != nil {
		return nil, err
	}
	if _, err := task.Delay(ctx, client, "later", async.WithCountdown(10*time.Minute)); err != nil {
		return nil, err
	}
	sig, err := task.Signature("periodic")
	if err != nil {
		return nil, err
	}
	if _, err := client.ApplyCanvas(ctx, async.Group(sig, sig)); err != nil {
		return nil, err
	}
	p := async.PeriodicSchedule{ID: async.StableID("preview", "hourly-reports"), Signature: sig, Rule: async.Every(time.Hour), Enabled: true, NextDue: time.Now().UTC().Add(time.Hour), Misfire: "coalesce", CatchUpLimit: 1, Overlap: "skip", Revision: 1}
	if err := m.UpsertSchedule(ctx, p, 0); err != nil {
		return nil, err
	}
	if err := m.ObserveBeat(ctx, "beat-preview", "offline", time.Second); err != nil {
		return nil, err
	}
	return async.NewDashboard(async.DashboardConfig{Client: client, Catalog: m, Title: "Async · Demo data", PageSize: 3, Authorize: func(r *http.Request) error {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || host != "127.0.0.1" && host != "localhost" {
			return async.ErrDenied
		}
		// Local fixture viewing only. Never replace application authentication
		// with this preview's permissive data policy.
		return nil
	}})
}
