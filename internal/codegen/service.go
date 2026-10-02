package codegen

import (
	"errors"
	"fmt"
	"os"
	"regexp"
)

var serviceName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`)

// StartService creates an opt-in composition root. It deliberately does not
// import config.Project or edit InstalledApps: either would couple independent
// services to the main backend's apps, optional modules and startup hooks.
func StartService(projectDir, name string) error {
	if len(name) > 63 || !serviceName.MatchString(name) {
		return errors.New("service name must contain lowercase letters, digits, hyphens or underscores and start with a letter (max 63 characters)")
	}
	// Keep generated paths portable to Windows, including CON/PRN device names.
	if regexp.MustCompile(`^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`).MatchString(name) {
		return errors.New("reserved service name")
	}
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err = projectModule(root); err != nil {
		return err
	}
	if info, err := root.Lstat("services"); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("services must be a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	files := map[string]string{
		"main.go": `package main
import "github.com/Newton-School/gogo"
func main() { gogo.Main(Project()) }
`,
		"project.go": fmt.Sprintf(`package main
import (
 "github.com/Newton-School/gogo"
 "github.com/Newton-School/gogo/core/app"
 "github.com/Newton-School/gogo/core/conf"
 "github.com/Newton-School/gogo/core/security"
 "github.com/Newton-School/gogo/core/urls"
 "net/http"
)
// Run from the project root. Add only this service's apps, resources and
// commands here. Shared configuration should live in a dependency-light package.
func Project() gogo.Project {
 return gogo.Project{
  Name: %q, Root: ".", MainPackage: %q, Schema: conf.CoreSchema(),
  Apps: []app.Config{},
  Handler: func(_ *app.Registry, settings conf.Values) (http.Handler, error) {
   router, err := urls.New(Routes()...)
   if err != nil { return nil, err }
   headers, err := security.Headers(security.HeadersConfig{AllowedHosts: settings.List("GOGO_ALLOWED_HOSTS")})
   if err != nil { return nil, err }
   return headers(router), nil
  },
 }
}
`, name, "./services/"+name),
		"urls.go": `package main
import (
 "net/http"
 ghttp "github.com/Newton-School/gogo/core/http"
 "github.com/Newton-School/gogo/core/urls"
)
// This process has no external dependencies yet. Add bounded dependency checks
// to readiness when resources are wired; liveness should stay dependency-free.
func Routes() []urls.Route {
 health := ghttp.Adapt(func(*http.Request) (ghttp.Response, error) {
  return ghttp.JSON(http.StatusOK, map[string]string{"status": "ok"})
 })
 return []urls.Route{
  urls.Path("health/live/", health, "live", "GET"),
  urls.Path("health/ready/", health, "ready", "GET"),
 }
}
`,
		"project_test.go": `package main
import (
 "net/http/httptest"
 "testing"
 "github.com/Newton-School/gogo/core/app"
)
func TestHealthWithoutExternalResources(t *testing.T) {
 project := Project()
 if len(project.RuntimeResources) != 0 { t.Fatal("update readiness tests when adding resources") }
 settings, err := project.Schema.Load(nil)
 if err != nil { t.Fatal(err) }
 handler, err := project.Handler(&app.Registry{}, settings)
 if err != nil { t.Fatal(err) }
 for _, path := range []string{"/health/live/", "/health/ready/"} {
  response := httptest.NewRecorder()
  handler.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost"+path, nil))
  if response.Code != 200 { t.Fatalf("%s: status %d", path, response.Code) }
 }
}
`,
		"README.md": "# " + name + "\n\nRun these commands from the project root:\n\n```sh\ngo run ./services/" + name + " serve\ngo run ./services/" + name + " runserver --reload\ngo test ./services/" + name + "/...\ngo build -trimpath -o bin/" + name + " ./services/" + name + "\n./bin/" + name + " serve\n```\n\nThis service shares the root go.mod and optional root .env. It requires no external resources by default. Wire only its own apps, routes, connections and commands in project.go. It does not mount the main backend or start workers implicitly. Keep shared models/migrations in apps/ and service-private implementation in this directory's internal/. Run shared migrations with the root manage binary. Use --addr or GOGO_HTTP_ADDR for a separate local port. Required resource settings are checked before connections open; malformed declared settings and unknown GOGO_ settings are still rejected.\n",
	}
	prepared, err := prepareFiles(files)
	if err != nil {
		return err
	}
	if err = root.MkdirAll("services", 0755); err != nil {
		return err
	}
	dir := "services/" + name
	// Exclusive directory creation rejects duplicates, existing files and links.
	if err = root.Mkdir(dir, 0755); err != nil {
		return fmt.Errorf("service destination already exists or cannot be created: %w", err)
	}
	serviceRoot, err := root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer serviceRoot.Close()
	return writePreparedTree(serviceRoot, prepared)
}
