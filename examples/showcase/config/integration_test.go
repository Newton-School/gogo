package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"example.com/gogo-showcase/apps/catalog"
	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo/async"
	"github.com/Newton-School/gogo/connectors/postgres"
	redistest "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/management"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// This test never uses the application's .env database. Explicit opt-in is
// mandatory. It owns one cryptographically random schema and one Redis child;
// cleanup drops only that schema, never the database or a shared Redis keyspace.
func TestNativeShowcaseJourney(t *testing.T) {
	dsn := os.Getenv("GOGO_SHOWCASE_TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("GOGO_TEST_REQUIRE_SERVICES") == "1" {
			t.Fatal("GOGO_SHOWCASE_TEST_POSTGRES_DSN is required for the showcase native gate")
		}
		t.Skip("set GOGO_SHOWCASE_TEST_POSTGRES_DSN to an explicitly disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owner, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxOpen: 2, MaxIdle: 1})
	if err != nil {
		t.Fatal("cannot connect to explicit test database")
	}
	t.Cleanup(func() { _ = owner.Close() })
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "showcase_test_" + hex.EncodeToString(random[:])
	quoted, err := owner.Dialect().QuoteIdentifier(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("cannot create owned test schema")
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := owner.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("cannot remove owned test schema")
		}
	})
	redis := redistest.Start(t)
	// Management intentionally rejects undeclared GOGO_* configuration. These
	// test-harness variables are not application settings; hide them only for
	// this non-parallel test and restore their original presence during cleanup.
	for _, name := range []string{"GOGO_TEST_REQUIRE_SERVICES", "GOGO_TEST_POSTGRES_DSN", "GOGO_SHOWCASE_TEST_POSTGRES_DSN"} {
		if value, exists := os.LookupEnv(name); exists {
			t.Setenv(name, value)
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	password := "Showcase-password-" + hex.EncodeToString(random[:])
	environment := map[string]string{
		"GOGO_ENV": "test", "GOGO_DATABASE_URL": dsn, "GOGO_SHOWCASE_SCHEMA": schema,
		"GOGO_REDIS_URL":  redis.URL,
		"GOGO_SECRET_KEY": password + password, "GOGO_SHOWCASE_ADMIN_IDENTIFIER": "showcase-admin", "GOGO_SHOWCASE_ADMIN_PASSWORD": password,
	}
	root := t.TempDir()
	call := func(args ...string) error {
		project := Project()
		project.Root, project.Environment = root, environment
		return management.Call(ctx, project, args, management.Options{Stdout: io.Discard, Stderr: io.Discard})
	}
	for _, args := range [][]string{{"migrate"}, {"migrate"}, {"seed"}, {"seed"}, {"createadmin"}} {
		if err := call(args...); err != nil {
			t.Fatalf("%s: %v", args[0], err)
		}
	}
	if err := call("createadmin"); err == nil {
		t.Fatal("createadmin overwrote an existing identity")
	}
	settings, err := Settings().Load(environment, "database", "redis", "auth", "sessions", "signing")
	if err != nil {
		t.Fatal(err)
	}
	connections := &Connections{}
	resources, err := connections.Resources(settings, []string{"database", "redis"})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.Bootstrap(ctx, InstalledApps(), resources, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = application.Close(cleanup)
	})
	count, err := orm.For(connections.Store, func() *catalog.Product { return &catalog.Product{} }).Count(ctx)
	if err != nil || count != 4 {
		t.Fatalf("idempotent seed products: %d %v", count, err)
	}
	verifyFieldRoundTrips(t, ctx, connections)
	handler, err := connections.Handler(application.Registry, settings)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	read := func(path string) (int, string) {
		t.Helper()
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		content, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(content)
	}
	if status, body := read("/api/v1/products/"); status != 200 || !strings.Contains(body, "Workspace notebook") || strings.Contains(body, "Unreleased desk lamp") {
		t.Fatalf("public API scope: %d %s", status, body)
	}
	if status, _ := read("/health/ready/"); status != 200 {
		t.Fatalf("readiness status %d", status)
	}
	if status, _ := read("/admin/"); status != 302 && status != 303 {
		t.Fatalf("anonymous Admin status %d", status)
	}
	if status, _ := read("/async/"); status != 303 {
		t.Fatalf("anonymous dashboard status %d", status)
	}
	status, login := read("/admin/login/")
	if status != 200 {
		t.Fatalf("login GET status %d", status)
	}
	token := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(login)
	if len(token) != 2 {
		t.Fatal("login CSRF token missing")
	}
	post := func(values url.Values) int {
		t.Helper()
		response, err := client.PostForm(server.URL+"/admin/login/", values)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if status := post(url.Values{"identifier": {"showcase-admin"}, "password": {password}}); status != 403 {
		t.Fatalf("missing CSRF accepted: %d", status)
	}
	if status := post(url.Values{"csrfmiddlewaretoken": {token[1]}, "identifier": {"showcase-admin"}, "password": {"wrong-password"}}); status != http.StatusUnauthorized {
		t.Fatalf("unexpected denied login status: %d", status)
	}
	if status, _ := read("/admin/catalog/product/"); status != http.StatusFound && status != http.StatusSeeOther {
		t.Fatalf("wrong password granted Admin access: %d", status)
	}
	if status := post(url.Values{"csrfmiddlewaretoken": {token[1]}, "identifier": {"showcase-admin"}, "password": {password}, "next": {"/admin/"}}); status != 302 && status != 303 {
		t.Fatalf("valid login status: %d", status)
	}
	if status, body := read("/admin/catalog/product/"); status != 200 || !strings.Contains(body, "Unreleased desk lamp") {
		t.Fatalf("authenticated Admin: %d %s", status, body)
	}
	if status, _ := read("/admin/fieldlab/specimen/"); status != 200 {
		t.Fatalf("field specimen Admin: %d", status)
	}
	if status, _ := read("/admin/fieldlab/specimen/add/"); status != 403 {
		t.Fatalf("specimen GET add unexpectedly allowed: %d", status)
	}
	status, adminPage := read("/admin/")
	if status != 200 {
		t.Fatalf("authorized Admin index status: %d", status)
	}
	adminToken := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(adminPage)
	if len(adminToken) != 2 {
		t.Fatal("authenticated Admin CSRF token missing")
	}
	denied, err := client.PostForm(server.URL+"/admin/fieldlab/specimen/add/", url.Values{"csrfmiddlewaretoken": {adminToken[1]}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, denied.Body)
	_ = denied.Body.Close()
	if denied.StatusCode != 403 {
		t.Fatalf("specimen POST add unexpectedly allowed: %d", denied.StatusCode)
	}
	specimenCount, err := orm.For(connections.Store, fieldlab.Factories()["fieldlab.Specimen"]).Count(ctx)
	if err != nil || specimenCount != 1 {
		t.Fatalf("denied specimen creation changed rows: %d %v", specimenCount, err)
	}
	if status, _ := read("/api/schema/"); status != 200 {
		t.Fatalf("OpenAPI status %d", status)
	}
	t.Run("configured_query_deadline", func(t *testing.T) {
		verifyConfiguredQueryDeadline(t, ctx, connections, application.Registry, environment)
	})
	for _, kind := range []string{"task", "delayed", "group", "chain", "chord"} {
		t.Run(kind, func(t *testing.T) {
			workerCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() {
				project := Project()
				project.Root, project.Environment = root, environment
				done <- management.Call(workerCtx, project, []string{"worker"}, management.Options{Stdout: io.Discard, Stderr: io.Discard})
			}()
			var output bytes.Buffer
			project := Project()
			project.Root, project.Environment = root, environment
			err := management.Call(ctx, project, []string{"demoasync", kind}, management.Options{Stdout: &output, Stderr: io.Discard})
			stop()
			select {
			case workerErr := <-done:
				if workerErr != nil && !errors.Is(workerErr, context.Canceled) {
					t.Errorf("worker: %v", workerErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("worker did not stop")
			}
			if err != nil {
				t.Fatalf("%s: %v", kind, err)
			}
			want := map[string]string{"task": `{"result":42}`, "delayed": `{"result":42}`, "group": `{"results":[6,10]}`, "chain": `{"results":[12]}`, "chord": `{"results":[16]}`}[kind]
			if !strings.HasSuffix(strings.TrimSpace(output.String()), want) {
				t.Fatalf("%s result incorrect: %s", kind, output.String())
			}
		})
	}
	t.Run("dashboard_and_beat", func(t *testing.T) {
		if err := call("demoasync", "schedule"); err != nil {
			t.Fatal(err)
		}
		r, err := connections.taskRuntime()
		if err != nil {
			t.Fatal(err)
		}
		pages, err := r.schedules.List(ctx, 10)
		if err != nil || len(pages) != 1 {
			t.Fatal(pages, err)
		}
		p := pages[0]
		expected := p.Revision
		p.Revision++
		p.NextDue = time.Now().UTC().Add(-time.Second)
		if err := r.schedules.UpsertSchedule(ctx, p, expected); err != nil {
			t.Fatal(err)
		}
		if err := call("beat", "--once"); err != nil {
			t.Fatal(err)
		}
		if err := call("worker", "--once"); err != nil {
			t.Fatal(err)
		}
		pages, err = r.schedules.List(ctx, 10)
		if err != nil || len(pages) != 1 || pages[0].LastTaskID == "" {
			t.Fatal(pages, err)
		}
		record, err := r.results.Lookup(ctx, pages[0].LastTaskID)
		if err != nil || record.State != async.Succeeded {
			t.Fatal(record.State, err)
		}
		for _, path := range []string{"/async/", "/async/tasks", "/async/tasks/" + record.Envelope.ID, "/async/workers", "/async/queues", "/async/beat", "/async/beat/" + p.ID, "/async/schedulers", "/async/workflows", "/async/events", "/async/style.css"} {
			status, body := read(path)
			if status != 200 {
				t.Fatalf("dashboard %s: %d %s", path, status, body)
			}
			if strings.Contains(body, password) {
				t.Fatal("credential in dashboard")
			}
		}
	})
}

func verifyConfiguredQueryDeadline(t *testing.T, ctx context.Context, connections *Connections, registry *app.Registry, environment map[string]string) {
	t.Helper()
	configured := make(map[string]string, len(environment)+1)
	for name, value := range environment {
		configured[name] = value
	}
	configured["GOGO_DB_QUERY_TIMEOUT"] = "100ms"
	settings, err := Settings().Load(configured, "database", "redis", "auth", "sessions", "signing")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := connections.Handler(registry, settings)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	transaction, err := connections.Database.BeginTx(ctx, db.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	schema, err := connections.Database.Dialect().QuoteIdentifier(environment["GOGO_SHOWCASE_SCHEMA"])
	if err != nil {
		t.Fatal(err)
	}
	table, err := connections.Database.Dialect().QuoteIdentifier((&catalog.Product{}).Schema().DBTable())
	if err != nil {
		t.Fatal(err)
	}
	// This relation belongs to the random test schema created above. No shared
	// application table, schema, Redis database or production record is locked.
	if _, err := transaction.Exec(ctx, "LOCK TABLE "+schema+"."+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	var blocker int64
	if err := db.QueryRow(ctx, transaction, "SELECT pg_backend_pid()", nil, &blocker); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get(server.URL + "/api/v1/products/")
		if err != nil {
			done <- result{err: err}
			return
		}
		defer response.Body.Close()
		content, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		done <- result{response.StatusCode, string(content), err}
	}()
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	waiting := func() bool {
		t.Helper()
		var blocked bool
		if err := db.QueryRow(probe, connections.Database, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.wait_event_type='Lock' AND $1::integer=ANY(pg_blocking_pids(a.pid)))", []any{blocker}, &blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !waiting() {
		select {
		case response := <-done:
			t.Fatalf("request never reached the owned native table lock: %d %v", response.status, response.err)
		case <-probe.Done():
			t.Fatal("native query lock was not observed")
		case <-tick.C:
		}
	}
	select {
	case response := <-done:
		if response.err != nil || response.status != 503 || response.body != "Request timed out" {
			t.Fatalf("configured request deadline: status=%d body=%q error=%v", response.status, response.body, response.err)
		}
	case <-probe.Done():
		t.Fatal("configured handler timeout did not terminate the response")
	}
	// The owning transaction still holds its lock. The waiter must disappear
	// through request-context cancellation, not because cleanup released it.
	for waiting() {
		select {
		case <-probe.Done():
			t.Fatal("timed-out database query remained blocked")
		case <-tick.C:
		}
	}
}

func verifyFieldRoundTrips(t *testing.T, ctx context.Context, connections *Connections) {
	t.Helper()
	factories := fieldlab.Factories()
	specimen, err := orm.For(connections.Store, factories["fieldlab.Specimen"]).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := models.Bind(specimen)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fieldlab.NewSpecimen()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range record.Schema().Fields {
		if field.IsAuto() {
			continue
		}
		value, err := record.Get(field.Name)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := want.Get(field.Name)
		if err != nil {
			t.Fatal(err)
		}
		value, err = field.Clean(ctx, value)
		if err != nil {
			t.Fatalf("%s stored value cannot be normalized: %v", field.Name, err)
		}
		expected, err = field.Clean(ctx, expected)
		if err != nil {
			t.Fatal(err)
		}
		if field.Kind == models.DateTime {
			if !value.(time.Time).Equal(expected.(time.Time)) {
				t.Errorf("%s changed instant", field.Name)
			}
			continue
		}
		if field.Kind == models.Date || field.Kind == models.Time {
			layout := "2006-01-02"
			if field.Kind == models.Time {
				layout = "15:04:05.999999"
			}
			if value.(time.Time).Format(layout) != expected.(time.Time).Format(layout) {
				t.Errorf("%s changed calendar/wall-clock value", field.Name)
			}
			continue
		}
		actualJSON, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		expectedJSON, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actualJSON, expectedJSON) {
			t.Errorf("%s changed across PostgreSQL: %s != %s", field.Name, actualJSON, expectedJSON)
		}
	}
	for _, name := range []string{"fieldlab.SmallIdentity", "fieldlab.StandardIdentity"} {
		model := factories[name]()
		if err := connections.Store.Save(ctx, model, orm.SaveOptions{ForceInsert: true}); err != nil {
			t.Fatal(err)
		}
		if err := connections.Store.RefreshFromDB(ctx, model); err != nil {
			t.Fatal(err)
		}
		bound, err := models.Bind(model)
		if err != nil {
			t.Fatal(err)
		}
		id, err := bound.Get("id")
		if err != nil || id == nil || id == int64(0) {
			t.Fatalf("%s missing generated identity", name)
		}
	}
	parentID, err := record.Get("id")
	if err != nil {
		t.Fatal(err)
	}
	related := factories["fieldlab.Related"]()
	bound, err := models.Bind(related)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"parent_id", "unique_parent_id"} {
		if err := bound.Set(field, parentID); err != nil {
			t.Fatal(err)
		}
	}
	if err := connections.Store.Save(ctx, related, orm.SaveOptions{ForceInsert: true}); err != nil {
		t.Fatal(err)
	}
	if err := connections.Store.RefreshFromDB(ctx, related); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"parent_id", "unique_parent_id", "peers"} {
		manager := orm.RelationManager{Store: connections.Store, Source: bound, Name: field,
			Scope:     func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("id__gt", 0), nil },
			Authorize: func(context.Context, orm.RelationChange) error { return nil },
		}
		if field == "peers" {
			if err := manager.Add(ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := manager.All(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("relation %s: %d %v", field, len(rows), err)
		}
	}
}
