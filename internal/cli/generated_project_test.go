package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/internal/version"
)

func TestGeneratedProjectWithAppCompilesAsDownstreamModule(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sampleproject")

	if err := NewStartprojectCommand().Run(context.Background(), []string{"sampleproject", target}); err != nil {
		t.Fatalf("startproject error = %v", err)
	}
	appTarget := filepath.Join(target, "apps", "blog")
	if err := NewStartappCommand().Run(context.Background(), []string{"blog", appTarget}); err != nil {
		t.Fatalf("startapp error = %v", err)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	runGeneratedCommand(t, target, "go", "mod", "edit", "-replace", "github.com/Newton-School/gogo="+filepath.ToSlash(repoRoot))
	writeTextFile(t, filepath.Join(target, ".env"), "GOGO_SECRET_KEY=generated-project-secret\nDATABASE_URL=sqlite://./db.sqlite3\n")
	writeTextFile(t, filepath.Join(target, "generated_runtime_test.go"), generatedRuntimeRouteTestSource())
	runGeneratedCommand(t, target, "go", "mod", "tidy")
	makemigrationsCheck := runGeneratedCommandOutput(t, target, "go", "run", "manage.go", "makemigrations", "--check", "--dry-run")
	if !strings.Contains(makemigrationsCheck, "no changes detected") {
		t.Fatalf("makemigrations check output = %q", makemigrationsCheck)
	}
	migrationPlan := runGeneratedCommandOutput(t, target, "go", "run", "manage.go", "migrate", "--plan")
	if !strings.Contains(migrationPlan, "apply blog.0001_initial") {
		t.Fatalf("migrate --plan output = %q", migrationPlan)
	}
	sqlmigrateOutput := runGeneratedCommandOutput(t, target, "go", "run", "manage.go", "sqlmigrate", "blog", "0001_initial")
	if !strings.Contains(sqlmigrateOutput, `CREATE TABLE "blog_item"`) {
		t.Fatalf("sqlmigrate output = %q", sqlmigrateOutput)
	}
	workerCheck := runGeneratedCommandOutput(t, target, "go", "run", "manage.go", "worker", "--check", "--broker-url", "memory://", "--result-backend", "memory")
	if !strings.Contains(workerCheck, "worker configured queues=default") {
		t.Fatalf("worker --check output = %q", workerCheck)
	}
	runGeneratedCommand(t, target, "go", "run", "manage.go", "migrate")
	runGeneratedCommand(t, target, "go", "run", "manage.go", "createsuperuser", "--username", "admin", "--email", "admin@example.com", "--password", "CorrectHorseBatteryStaple42", "--noinput")
	inspectOutput := runGeneratedCommandOutput(t, target, "go", "run", "manage.go", "inspect", "--report")
	if !strings.Contains(inspectOutput, "registered=1") {
		t.Fatalf("project-aware inspect output = %q, want registered task", inspectOutput)
	}
	checkOutput := runGeneratedCommandOutput(t, target, "go", "run", "manage.go", "check", "--tag", "blog")
	if !strings.Contains(checkOutput, "INFO blog blog app checks registered") {
		t.Fatalf("project-aware check output = %q, want app check", checkOutput)
	}
	runGeneratedCommand(t, target, "go", "run", "manage.go", "blog.reindex", "--all")
	runGeneratedCommand(t, target, "go", "test", "./...")
	assertNoInternalFrameworkImports(t, target)
}

func TestGeneratedProjectCanRunStartappBeforeManualTidy(t *testing.T) {
	oldVersion := version.Version
	version.Version = "0.3.0"
	defer func() { version.Version = oldVersion }()

	target := filepath.Join(t.TempDir(), "sampleproject")
	if err := NewStartprojectCommand().Run(context.Background(), []string{"sampleproject", target}); err != nil {
		t.Fatalf("startproject error = %v", err)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	runGeneratedCommand(t, target, "go", "mod", "edit", "-replace", "github.com/Newton-School/gogo="+filepath.ToSlash(repoRoot))
	runGeneratedCommand(t, target, "go", "run", "manage.go", "startapp", "blog", "apps/blog")
}

func generatedRuntimeRouteTestSource() string {
	return `package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	project "sampleproject/sampleproject"
)

func TestGeneratedRouterMountsAppHTTPAPIAndAdminRoutes(t *testing.T) {
	router, err := project.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	for _, test := range []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},
		{"/raw/sample/", http.StatusOK},
		{"/blog/", http.StatusOK},
		{"/api/blog/items/", http.StatusOK},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s status = %d body=%s, want %d", test.path, response.Code, response.Body.String(), test.want)
		}
	}

	anonymousAdmin := httptest.NewRecorder()
	router.ServeHTTP(anonymousAdmin, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if anonymousAdmin.Code != http.StatusFound || anonymousAdmin.Header().Get("Location") != "/admin/login/?next=%2Fadmin%2F" {
		t.Fatalf("anonymous admin response = %d location=%q", anonymousAdmin.Code, anonymousAdmin.Header().Get("Location"))
	}

	loginPage := httptest.NewRecorder()
	router.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/admin/login/", nil))
	if loginPage.Code != http.StatusOK {
		t.Fatalf("login page status = %d", loginPage.Code)
	}
	csrfToken := extractCSRFToken(t, loginPage.Body.String())

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "CorrectHorseBatteryStaple42")
	form.Set("next", "/admin/")
	form.Set("csrfmiddlewaretoken", csrfToken)
	loginRequest := httptest.NewRequest(http.MethodPost, "/admin/login/", strings.NewReader(form.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range loginPage.Result().Cookies() {
		loginRequest.AddCookie(cookie)
	}
	loginResponse := httptest.NewRecorder()
	router.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusFound || loginResponse.Header().Get("Location") != "/admin/" {
		t.Fatalf("login response = %d location=%q body=%s", loginResponse.Code, loginResponse.Header().Get("Location"), loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("login did not set admin session cookie")
	}

	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	for _, cookie := range cookies {
		authenticatedRequest.AddCookie(cookie)
	}
	authenticatedAdmin := httptest.NewRecorder()
	router.ServeHTTP(authenticatedAdmin, authenticatedRequest)
	if authenticatedAdmin.Code != http.StatusOK || !strings.Contains(authenticatedAdmin.Body.String(), "Site administration") {
		t.Fatalf("authenticated admin response = %d body=%s", authenticatedAdmin.Code, authenticatedAdmin.Body.String())
	}
	for _, want := range []string{"/admin/auth/user/", "/admin/auth/group/", "/admin/auth/permission/"} {
		if !strings.Contains(authenticatedAdmin.Body.String(), want) {
			t.Fatalf("authenticated admin body missing %q:\n%s", want, authenticatedAdmin.Body.String())
		}
	}
}

var csrfInputPattern = regexp.MustCompile("name=\"csrfmiddlewaretoken\" value=\"([^\"]+)\"")

func extractCSRFToken(t *testing.T, body string) string {
	t.Helper()
	matches := csrfInputPattern.FindStringSubmatch(body)
	if len(matches) != 2 || matches[1] == "" {
		t.Fatalf("login page did not render csrf token:\n%s", body)
	}
	return matches[1]
}
`
}

func assertNoInternalFrameworkImports(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), "github.com/Newton-School/gogo/internal") {
			t.Fatalf("generated file imports internal framework package: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk generated project: %v", err)
	}
}

func runGeneratedCommand(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	output := runGeneratedCommandOutput(t, dir, name, args...)
	_ = output
}

func runGeneratedCommandOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed in %s: %v\n%s", name, strings.Join(args, " "), dir, err, output)
	}
	return string(output)
}
