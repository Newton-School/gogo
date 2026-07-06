package management

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/conf"
	gogohttp "github.com/Newton-School/gogo/http"
	"github.com/Newton-School/gogo/internal/cli"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/migrations/operations"
	"github.com/Newton-School/gogo/queue"

	_ "modernc.org/sqlite"
)

func TestExecuteRunsBuiltInCommands(t *testing.T) {
	var stdout bytes.Buffer
	if err := Execute(context.Background(), []string{"help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Execute(help) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "runserver") || !strings.Contains(stdout.String(), "makemigrations") {
		t.Fatalf("help output missing management commands:\n%s", stdout.String())
	}
}

func TestExecuteProjectUsesProjectQueueApp(t *testing.T) {
	queueApp := queue.NewApp(queue.AppOptions{})
	_, err := queueApp.RegisterTask("blog.example", func(context.Context, ...any) (any, error) {
		return "ok", nil
	}, queue.TaskOptions{})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}

	var stdout bytes.Buffer
	err = ExecuteProject(context.Background(), []string{"inspect", "--report"}, &stdout, &bytes.Buffer{}, Project{
		QueueApp: func() *queue.App {
			return queueApp
		},
	})
	if err != nil {
		t.Fatalf("ExecuteProject(inspect) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "registered=1") {
		t.Fatalf("inspect output missing registered task count:\n%s", stdout.String())
	}
}

func TestExecuteProjectRunsProjectCommand(t *testing.T) {
	var called bool
	var gotArgs []string
	command := projectCommand{
		name:    "blog.reindex",
		summary: "Reindex blog content",
		run: func(_ context.Context, args []string) error {
			called = true
			gotArgs = append([]string(nil), args...)
			return nil
		},
	}

	var stdout bytes.Buffer
	err := ExecuteProject(context.Background(), []string{"blog.reindex", "--all"}, &stdout, &bytes.Buffer{}, Project{
		Commands: func() []Command {
			return []Command{command}
		},
	})
	if err != nil {
		t.Fatalf("ExecuteProject(custom command) error = %v", err)
	}
	if !called || strings.Join(gotArgs, ",") != "--all" {
		t.Fatalf("custom command called=%v args=%#v", called, gotArgs)
	}

	if err := ExecuteProject(context.Background(), []string{"check"}, &stdout, &bytes.Buffer{}, Project{
		Commands: func() []Command {
			return []Command{projectCommand{name: "check", summary: "Bad", run: func(context.Context, []string) error { return nil }}}
		},
	}); !errors.Is(err, cli.ErrDuplicateCommand) {
		t.Fatalf("ExecuteProject(duplicate command) error = %v, want ErrDuplicateCommand", err)
	}
}

func TestExecuteProjectRunsProjectChecks(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GOGO_SECRET_KEY=management-secret\nDATABASE_URL=sqlite://:memory:\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	project := Project{
		Checks: func() []checks.Check {
			return []checks.Check{{
				ID:       "project.E001",
				Tags:     []string{"project"},
				Severity: checks.SeverityError,
				Message:  "PROJECT_REQUIRED is configured",
				Run: func(context.Context) checks.Result {
					if os.Getenv("PROJECT_REQUIRED") == "" {
						return checks.Result{ID: "project.E001", Tags: []string{"project"}, Severity: checks.SeverityError, Message: "PROJECT_REQUIRED is required"}
					}
					return checks.Result{ID: "project.I001", Tags: []string{"project"}, Severity: checks.SeverityInfo, Message: "project settings valid"}
				},
			}}
		},
	}

	var stdout bytes.Buffer
	err := ExecuteProject(context.Background(), []string{"check", "--tag", "project"}, &stdout, &bytes.Buffer{}, project)
	if !errors.Is(err, cli.ErrCommandFailed) {
		t.Fatalf("ExecuteProject(check) error = %v, want ErrCommandFailed", err)
	}
	if !strings.Contains(stdout.String(), "ERROR project PROJECT_REQUIRED is required") {
		t.Fatalf("check output missing project failure:\n%s", stdout.String())
	}

	t.Setenv("PROJECT_REQUIRED", "configured")
	stdout.Reset()
	if err := ExecuteProject(context.Background(), []string{"check", "--tag", "project"}, &stdout, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("ExecuteProject(check configured) error = %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "INFO project project settings valid") {
		t.Fatalf("check output missing project pass:\n%s", stdout.String())
	}
}

func TestExecuteProjectUsesCompiledMigrations(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	dbPath := filepath.Join(dir, "db.sqlite3")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GOGO_SECRET_KEY=management-secret\nDATABASE_URL=sqlite://"+filepath.ToSlash(dbPath)+"\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	project := Project{
		Migrations: func() []migrations.Migration {
			return []migrations.Migration{{
				AppLabel: "blog",
				Name:     migrations.InitialMigrationName(),
				Atomic:   true,
				Operations: []migrations.Operation{
					operations.RunSQL{SQL: "CREATE TABLE blog_compiled (id integer PRIMARY KEY)"},
				},
			}}
		},
	}

	var sqlOut bytes.Buffer
	if err := ExecuteProject(context.Background(), []string{"sqlmigrate", "blog", "0001_initial"}, &sqlOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("ExecuteProject(sqlmigrate) error = %v", err)
	}
	if !strings.Contains(sqlOut.String(), "CREATE TABLE blog_compiled") {
		t.Fatalf("sqlmigrate did not use compiled migration:\n%s", sqlOut.String())
	}

	var migrateOut bytes.Buffer
	if err := ExecuteProject(context.Background(), []string{"migrate", "--app", "blog"}, &migrateOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("ExecuteProject(migrate) error = %v\n%s", err, migrateOut.String())
	}
	assertManagementSQLiteTableExists(t, dbPath, "blog_compiled")
}

func TestProjectBuildServerUsesCustomMiddlewareAndServerConfig(t *testing.T) {
	settings := validManagementSettings()
	settings.Middleware = []string{"project.HeaderMiddleware"}
	project := Project{
		Router: func() (*gogohttp.Router, error) {
			router := gogohttp.NewRouter()
			if err := router.Handle("home", "/", func(context.Context, *gogohttp.Request) gogohttp.Response {
				return gogohttp.Text(http.StatusOK, "home")
			}); err != nil {
				return nil, err
			}
			return router, nil
		},
		Middleware: func(conf.Settings) (gogohttp.MiddlewareRegistry, error) {
			return gogohttp.MiddlewareRegistry{
				"project.HeaderMiddleware": func(conf.Settings) gogohttp.Middleware {
					return func(next gogohttp.Handler) gogohttp.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							w.Header().Set("X-Project-Middleware", "yes")
							next.ServeHTTP(w, r)
						})
					}
				},
			}, nil
		},
		ServerConfig: func(conf.Settings) gogohttp.ServerConfig {
			return gogohttp.ServerConfig{HealthPath: "/readyz/"}
		},
	}

	server, err := project.buildServer(context.Background(), io.Discard, cli.RunserverConfig{Settings: settings, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("buildServer() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "home" || recorder.Header().Get("X-Project-Middleware") != "yes" {
		t.Fatalf("home response = %d body=%q middleware=%q", recorder.Code, recorder.Body.String(), recorder.Header().Get("X-Project-Middleware"))
	}

	ready := httptest.NewRecorder()
	server.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz/", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("custom health status = %d", ready.Code)
	}
}

func assertManagementSQLiteTableExists(t *testing.T, dbPath, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
		t.Fatalf("expected sqlite table %s: %v", table, err)
	}
}

func TestProjectServerStarterReadyFailurePreventsServerStart(t *testing.T) {
	readyErr := errors.New("ready failed")
	project := Project{
		Ready: func(context.Context) error {
			return readyErr
		},
	}
	err := project.serverStarter(io.Discard)(context.Background(), cli.RunserverConfig{Settings: validManagementSettings(), Addr: "127.0.0.1:0"})
	if !errors.Is(err, readyErr) {
		t.Fatalf("serverStarter error = %v, want readyErr", err)
	}
}

func TestProjectServerStarterRunsShutdownAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var shutdownCalls int
	project := Project{
		Ready: func(context.Context) error {
			cancel()
			return nil
		},
		Shutdown: func(context.Context) error {
			shutdownCalls++
			return nil
		},
		ServerConfig: func(conf.Settings) gogohttp.ServerConfig {
			return gogohttp.ServerConfig{ShutdownTimeout: 100 * time.Millisecond}
		},
	}

	err := project.serverStarter(io.Discard)(ctx, cli.RunserverConfig{Settings: validManagementSettings(), Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("serverStarter error = %v", err)
	}
	if shutdownCalls != 1 {
		t.Fatalf("Shutdown hook calls = %d, want 1", shutdownCalls)
	}
}

func TestProjectServerStarterRunsShutdownWithoutReadyHook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var shutdownCalled bool
	project := Project{
		Shutdown: func(context.Context) error {
			shutdownCalled = true
			return nil
		},
	}

	err := project.serverStarter(io.Discard)(ctx, cli.RunserverConfig{Settings: validManagementSettings(), Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("serverStarter error = %v", err)
	}
	if !shutdownCalled {
		t.Fatal("Shutdown hook was not called")
	}
}

func TestExecuteProjectCreateSuperuserPersistsToAuthUserTable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GOGO_SECRET_KEY=management-secret\nDATABASE_URL=sqlite://"+filepath.ToSlash(dbPath)+"\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	project := Project{ModelMetadata: auth.ModelMetadata}
	if err := ExecuteProject(context.Background(), []string{"migrate"}, &bytes.Buffer{}, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("migrate error = %v", err)
	}
	var stdout bytes.Buffer
	if err := ExecuteProject(context.Background(), []string{
		"createsuperuser",
		"--username", "admin",
		"--email", "admin@example.com",
		"--password", "CorrectHorseBatteryStaple42",
		"--noinput",
	}, &stdout, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("createsuperuser error = %v", err)
	}
	if !strings.Contains(stdout.String(), "created superuser admin on database default") {
		t.Fatalf("createsuperuser stdout = %q", stdout.String())
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var email string
	var isStaff, isSuperuser bool
	if err := db.QueryRow(`SELECT email, is_staff, is_superuser FROM auth_user WHERE username = 'admin'`).Scan(&email, &isStaff, &isSuperuser); err != nil {
		t.Fatalf("query auth_user: %v", err)
	}
	if email != "admin@example.com" || !isStaff || !isSuperuser {
		t.Fatalf("auth_user row = email:%q staff:%v superuser:%v", email, isStaff, isSuperuser)
	}
}

type projectCommand struct {
	name    string
	summary string
	run     func(context.Context, []string) error
}

func (c projectCommand) Name() string {
	return c.name
}

func (c projectCommand) Summary() string {
	return c.summary
}

func (c projectCommand) Run(ctx context.Context, args []string) error {
	return c.run(ctx, args)
}

func validManagementSettings() conf.Settings {
	settings := conf.DefaultSettings()
	settings.SecretKey = "management-secret"
	settings.DatabaseURL = "sqlite://:memory:"
	settings.HTTPAddr = "127.0.0.1:0"
	return settings
}
