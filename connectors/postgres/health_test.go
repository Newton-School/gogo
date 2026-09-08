package postgres_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/health"
)

func TestRealPostgresHealthSeparatesLifecycleRoleAndDependencyState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection := openTest(t)
	ping := connection.Ping
	var webCalls, workerCalls atomic.Int32
	var draining atomic.Bool
	state := func() health.State {
		return health.State{Live: true, Started: true, Ready: !draining.Load(), Stopping: draining.Load()}
	}
	dependencies := []health.Dependency{
		{ID: "primary", Roles: []string{"web"}, Probe: func(ctx context.Context) error { webCalls.Add(1); return ping(ctx) }},
		{ID: "worker_only", Roles: []string{"worker"}, Probe: func(ctx context.Context) error { workerCalls.Add(1); return ping(ctx) }},
	}
	newChecker := func(role string, dependencies []health.Dependency) *health.Checker {
		t.Helper()
		checker, err := health.New(health.Config{Role: role, State: state, Dependencies: dependencies, DisableCache: true, Timeout: 3 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return checker
	}
	check := func(checker *health.Checker, kind health.Kind, want health.Status) health.Report {
		t.Helper()
		report, err := checker.Check(ctx, kind)
		if err != nil || report.Kind != kind || report.Status != want || report.Cached {
			t.Fatalf("health %s: status=%s cached=%t error=%v", kind, report.Status, report.Cached, err)
		}
		return report
	}
	web, worker := newChecker("web", dependencies), newChecker("worker", dependencies)
	for _, checker := range []*health.Checker{web, worker} {
		for _, test := range []struct {
			kind health.Kind
			want health.Status
		}{{health.KindLive, health.StatusLive}, {health.KindStartup, health.StatusStarted}} {
			if report := check(checker, test.kind, test.want); len(report.Checks) != 0 {
				t.Fatal("local lifecycle check included dependencies", report.Checks)
			}
		}
	}
	if webCalls.Load() != 0 || workerCalls.Load() != 0 {
		t.Fatal("liveness/startup pinged an external dependency")
	}
	report := check(web, health.KindReady, health.StatusReady)
	if report.Role != "web" || len(report.Checks) != 1 || report.Checks[0].ID != "primary" || report.Checks[0].Status != health.CheckPassed || webCalls.Load() != 1 || workerCalls.Load() != 0 {
		t.Fatal("web selected an incorrect dependency", report)
	}
	report = check(worker, health.KindReady, health.StatusReady)
	if report.Role != "worker" || len(report.Checks) != 1 || report.Checks[0].ID != "worker_only" || report.Checks[0].Status != health.CheckPassed || webCalls.Load() != 1 || workerCalls.Load() != 1 {
		t.Fatal("worker selected an incorrect dependency", report)
	}

	// Admission stops before any dependency is contacted, even while the owned
	// connection is healthy. This does not close an application-owned resource.
	draining.Store(true)
	report = check(web, health.KindReady, health.StatusNotReady)
	if len(report.Checks) != 0 || webCalls.Load() != 1 || workerCalls.Load() != 1 {
		t.Fatal("draining readiness performed an external probe", report)
	}
	if err := ping(ctx); err != nil {
		t.Fatal("health checking changed resource ownership", err)
	}
	draining.Store(false)

	// Close only this disposable client pool, never the database/server.
	// A real connector failure must not be reported as process death.
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	report = check(web, health.KindReady, health.StatusNotReady)
	if len(report.Checks) != 1 || report.Checks[0].Status != health.CheckFailed || report.Checks[0].Optional || report.Checks[0].Bypassed || webCalls.Load() != 2 {
		t.Fatal("required closed connection did not fail readiness", report)
	}
	for _, checker := range []*health.Checker{web, worker} {
		check(checker, health.KindLive, health.StatusLive)
		check(checker, health.KindStartup, health.StatusStarted)
	}
	if webCalls.Load() != 2 || workerCalls.Load() != 1 {
		t.Fatal("local probes contacted a failed external dependency")
	}

	// Optional is descriptive until the application's explicit bypass policy
	// permits degraded readiness. Both cases exercise the actual closed pool.
	for _, allow := range []bool{false, true} {
		checker := newChecker("web", []health.Dependency{{ID: "optional", Probe: ping, Optional: true, AllowDegraded: allow}})
		want := health.StatusNotReady
		if allow {
			want = health.StatusDegraded
		}
		report = check(checker, health.KindReady, want)
		if len(report.Checks) != 1 || report.Checks[0].Status != health.CheckFailed || !report.Checks[0].Optional || report.Checks[0].Bypassed != allow || report.Success() != allow {
			t.Fatal("optional dependency silently bypassed policy", report)
		}
	}
}
