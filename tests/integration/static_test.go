package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/management"
	"github.com/Newton-School/gogo/core/static"
	"github.com/Newton-School/gogo/core/templates"
)

func TestStaticProjectCommandsTemplatesAndDevelopment(t *testing.T) {
	// This fixture uses real local files and the public central command entry
	// point. No external database, cache, or alternate collector is involved.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	parent := t.TempDir()
	projectRoot := filepath.Join(parent, "project-assets")
	appRoot := filepath.Join(parent, "app-assets")
	destination := filepath.Join(parent, "collected")
	write := func(root, name, contents string) {
		t.Helper()
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(projectRoot, "css/app.css", `body{background:url(../images/icon.svg?v=1#icon)}`)
	write(projectRoot, "images/icon.svg", "project image")
	write(appRoot, "images/icon.svg", "app image")
	write(appRoot, "js/app.js", "console.log('public')")
	write(projectRoot, ".env", "never public")
	configs := []app.Config{{Name: "example.assets", Label: "assets", Register: func(registry *app.Registry) error {
		return static.Register(registry, "assets", static.Source{Directory: appRoot})
	}, Ready: func(context.Context, *app.Registry) error { t.Fatal("Ready ran"); return nil }}}
	resolve := func(_ context.Context, registry *app.Registry, settings conf.Values) (*static.Collector, error) {
		sources, err := static.AppSources(registry, configs)
		if err != nil {
			return nil, err
		}
		return static.New(static.Config{Sources: append([]static.Source{{Owner: "project", Directory: projectRoot}}, sources...), Destination: settings.String("GOGO_STATIC_ROOT"), BaseURL: "/assets/"})
	}
	project := management.Project{Root: parent, Apps: configs, Environment: map[string]string{"GOGO_STATIC_ROOT": destination}, Commands: management.StaticCommands(resolve), ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { t.Fatal("services opened"); return nil, nil }}
	call := func(args ...string) []byte {
		t.Helper()
		var stdout bytes.Buffer
		if err := management.Call(context.Background(), project, args, management.Options{Stdout: &stdout}); err != nil {
			t.Fatal(err)
		}
		return stdout.Bytes()
	}
	var found struct {
		Matches []static.Match `json:"matches"`
	}
	if err := json.Unmarshal(call("findstatic", "images/icon.svg"), &found); err != nil || len(found.Matches) != 2 || !found.Matches[0].Selected || found.Matches[0].Owner != "project" {
		t.Fatal(found, err)
	}
	call("collectstatic", "--dry-run")
	if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("dry run wrote output", err)
	}
	call("collectstatic")
	manifest, err := static.LoadManifest(context.Background(), destination, "/assets/")
	if err != nil {
		t.Fatal(err)
	}
	cssURL, err := manifest.URL("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	iconURL, err := manifest.URL("images/icon.svg")
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile(filepath.Join(destination, strings.TrimPrefix(cssURL, "/assets/")))
	if err != nil || !strings.Contains(string(css), "../"+strings.TrimPrefix(iconURL, "/assets/")+"?v=1#icon") {
		t.Fatal(string(css), err)
	}
	icon, err := os.ReadFile(filepath.Join(destination, strings.TrimPrefix(iconURL, "/assets/")))
	if err != nil || string(icon) != "project image" {
		t.Fatal(string(icon), err)
	}
	engine := templates.New(templates.Config{Strict: true, Tags: manifest.Tags(), Libraries: []string{"static"}})
	markup, err := engine.RenderString(context.Background(), `{% load static %}{% static "css/app.css" as style %}<link href="{{ style }}">`, nil)
	if err != nil || !strings.Contains(markup, cssURL) {
		t.Fatal(markup, err)
	}
	write(projectRoot, "images/icon.svg", "new image")
	call("collectstatic")
	updated, err := static.LoadManifest(context.Background(), destination, "/assets/")
	if err != nil {
		t.Fatal(err)
	}
	newCSS, err := updated.URL("css/app.css")
	if err != nil || newCSS == cssURL {
		t.Fatal(newCSS, err)
	}
	if _, err := os.Stat(filepath.Join(destination, strings.TrimPrefix(cssURL, "/assets/"))); err != nil {
		t.Fatal("old hash lost", err)
	}
	write(projectRoot, "css/app.css", `body{background:url(missing.png)}`)
	var failed bytes.Buffer
	if err := management.Call(context.Background(), project, []string{"collectstatic"}, management.Options{Stdout: &failed}); err == nil || failed.Len() != 0 {
		t.Fatal(err)
	}
	current, err := static.LoadManifest(context.Background(), destination, "/assets/")
	if err != nil || !bytes.Equal(current.JSON(), updated.JSON()) {
		t.Fatal("failed publish changed manifest", err)
	}
	application, err := app.Prepare(configs, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := conf.CoreSchema().Load(project.Environment)
	if err != nil {
		t.Fatal(err)
	}
	collector, err := resolve(context.Background(), application.Registry, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.DevHandler(false); !errors.Is(err, static.ErrNotDebug) {
		t.Fatal(err)
	}
	handler, err := collector.DevHandler(true)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest("GET", "http://example.test/assets/images/icon.svg", nil))
	if first.Code != 200 || first.Body.String() != "new image" {
		t.Fatal(first.Code, first.Body.String())
	}
	request := httptest.NewRequest("HEAD", "http://example.test/assets/images/icon.svg", nil)
	request.Header["If-None-Match"] = []string{`"other"`, first.Header().Get("ETag")}
	conditional := httptest.NewRecorder()
	handler.ServeHTTP(conditional, request)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatal(conditional.Code, conditional.Body.String())
	}
	private := httptest.NewRecorder()
	handler.ServeHTTP(private, httptest.NewRequest("GET", "http://example.test/assets/.env", nil))
	if private.Code != 404 || strings.Contains(private.Body.String(), "never public") {
		t.Fatal(private.Code)
	}
}
