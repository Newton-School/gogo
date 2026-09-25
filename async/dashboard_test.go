package async_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func dashboardFixture(t *testing.T, policy func(context.Context, string, string, string) error) (async.DashboardConfig, *async.Task[string, string], *async.Worker, *fakes.Memory) {
	t.Helper()
	m := fakes.NewMemory()
	reg := async.NewRegistry()
	task, err := async.Register(reg, "mail.send", 1, func(context.Context, async.TaskContext, string) (string, error) { return "SECRET_RESULT", nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if policy == nil {
		policy = func(context.Context, string, string, string) error { return nil }
	}
	client, err := async.NewClient(async.ClientConfig{Registry: reg, Broker: m, Results: m, Workflows: m, Schedules: m, Presence: m, Queues: []string{"default"}, Authorize: policy})
	if err != nil {
		t.Fatal(err)
	}
	return async.DashboardConfig{Client: client, Catalog: m, Authorize: func(*http.Request) error { return nil }}, task, &async.Worker{Registry: reg, Client: client, Broker: m, Results: m, ID: "worker-1", Queues: []string{"default"}, Presence: m}, m
}
func dashboardRequest(t *testing.T, c async.DashboardConfig, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	h, err := async.NewDashboard(c)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestDashboardPagesAndPrivatePayloads(t *testing.T) {
	c, task, worker, m := dashboardFixture(t, nil)
	ctx := context.Background()
	r, err := task.Delay(ctx, c.Client, "SECRET_ARGUMENT", async.WithScope("tenant", "SECRET_PRINCIPAL"))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	sig, _ := task.Signature("SECRET_SCHEDULE")
	p := async.PeriodicSchedule{ID: async.StableID("dashboard", "schedule"), Signature: sig, Rule: async.Every(time.Hour), Enabled: true, NextDue: time.Now().Add(time.Hour), Misfire: "skip", Overlap: "allow", CatchUpLimit: 1, Revision: 1}
	if err := m.UpsertSchedule(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.ObserveBeat(ctx, "beat-1", "online", time.Minute); err != nil {
		t.Fatal(err)
	}
	g, err := c.Client.ApplyCanvas(ctx, async.Group(sig))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"", "tasks", "tasks/" + r.Receipt.ID, "workers", "workers/worker-1", "queues", "beat", "beat/" + p.ID, "schedulers", "schedulers/beat-1", "workflows", "workflows/" + g.ID, "events"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			w := dashboardRequest(t, c, "GET", "/async/"+path)
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			for _, secret := range []string{"SECRET_", "<script", "name=\"csrf"} {
				if strings.Contains(body, secret) {
					t.Fatal("private or executable content", secret)
				}
			}
			if !strings.Contains(body, "Read-only monitoring") || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("missing security UI/headers")
			}
		})
	}
	for _, tt := range []struct {
		method, path string
		status       int
	}{{"HEAD", "/async/tasks", 200}, {"POST", "/async/tasks", 405}, {"GET", "/async", 308}, {"GET", "/async/nothing", 404}, {"GET", "/async/tasks/bad", 400}, {"GET", "/async/tasks?state=BOGUS", 400}, {"GET", "/async/tasks?q=a&q=b", 400}, {"GET", "/async/tasks?cursor=bad", 400}, {"GET", "/async/style.css", 200}, {"GET", "/async/tasks/" + async.StableID("missing", "task"), 404}} {
		w := dashboardRequest(t, c, tt.method, tt.path)
		if w.Code != tt.status {
			t.Fatalf("%s %s: %d", tt.method, tt.path, w.Code)
		}
		if tt.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
}

func TestDashboardAuthorizationAndNoPartialData(t *testing.T) {
	policyError := error(nil)
	c, task, _, m := dashboardFixture(t, func(_ context.Context, action, scope, id string) error {
		if action == "inspect" {
			return policyError
		}
		return nil
	})
	r, err := task.Delay(context.Background(), c.Client, "SECRET")
	if err != nil {
		t.Fatal(err)
	}
	policyError = async.ErrDenied
	w := dashboardRequest(t, c, "GET", "/async/tasks")
	if w.Code != 200 || strings.Contains(w.Body.String(), r.Receipt.ID) {
		t.Fatal("denied record leaked")
	}
	w = dashboardRequest(t, c, "GET", "/async/tasks/"+r.Receipt.ID)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	policyError = errors.New("SECRET_POLICY")
	w = dashboardRequest(t, c, "GET", "/async/tasks")
	if w.Code != 503 || strings.Contains(w.Body.String(), r.Receipt.ID) || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal("outage exposed partial data")
	}
	c.Authorize = func(*http.Request) error { return async.ErrDenied }
	for _, path := range []string{"/async/", "/async/style.css", "/async/tasks"} {
		if w := dashboardRequest(t, c, "GET", path); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	c.Authorize = func(*http.Request) error { panic("SECRET_PANIC") }
	w = dashboardRequest(t, c, "GET", "/async/")
	if w.Code != 503 || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal(w.Code)
	}
	_ = m
}

type dashboardFaultCatalog struct {
	async.DashboardCatalog
	calls      int
	loop, fail bool
}

func (c *dashboardFaultCatalog) ListDashboard(ctx context.Context, kind, cursor string, limit int) (async.DashboardIDs, error) {
	c.calls++
	if c.fail {
		panic("SECRET_BACKEND")
	}
	if c.loop {
		return async.DashboardIDs{NextCursor: "same"}, nil
	}
	return c.DashboardCatalog.ListDashboard(ctx, kind, cursor, limit)
}

func TestDashboardPaginationFiltersAndBoundaries(t *testing.T) {
	c, task, _, m := dashboardFixture(t, nil)
	ctx := context.Background()
	c.PageSize = 1
	for i := 0; i < 3; i++ {
		if _, err := task.Delay(ctx, c.Client, "SECRET"); err != nil {
			t.Fatal(err)
		}
	}
	w := dashboardRequest(t, c, "GET", "/async/tasks")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Next page") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = dashboardRequest(t, c, "GET", "/async/tasks?q=%3Cscript%3E")
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>") || !strings.Contains(w.Body.String(), "&lt;script&gt;") {
		t.Fatal("escaping", w.Code)
	}
	fault := &dashboardFaultCatalog{DashboardCatalog: m, loop: true}
	c.Catalog = fault
	w = dashboardRequest(t, c, "GET", "/async/tasks")
	if w.Code != 503 || fault.calls != 2 {
		t.Fatal("unbounded cursor", w.Code, fault.calls)
	}
	fault.loop = false
	fault.fail = true
	w = dashboardRequest(t, c, "GET", "/async/tasks")
	if w.Code != 503 || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal("panic exposed")
	}
	c.Catalog = m
	c.Authorize = nil
	if _, err := async.NewDashboard(c); err != async.ErrInvalid {
		t.Fatal("missing gate accepted")
	}
	c.Authorize = func(*http.Request) error { return nil }
	c.BasePath = "//evil/"
	if _, err := async.NewDashboard(c); err != async.ErrInvalid {
		t.Fatal("bad base path accepted")
	}
}

type beatMonitorFailure struct{}

func (beatMonitorFailure) ObserveBeat(context.Context, string, string, time.Duration) error {
	panic("SECRET_MONITOR")
}
func TestDashboardBeatTelemetryDoesNotChangeTick(t *testing.T) {
	c, _, _, m := dashboardFixture(t, nil)
	beat := &async.Beat{Client: c.Client, Store: m, ID: "beat", Monitor: beatMonitorFailure{}}
	if err := beat.Tick(context.Background()); err != nil {
		t.Fatal("telemetry changed empty tick", err)
	}
	beat.Monitor = m
	if err := beat.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := m.ReadDashboardBeat(context.Background(), "beat")
	if err != nil || p.Status != "online" {
		t.Fatal(p, err)
	}
}
