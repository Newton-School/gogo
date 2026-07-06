package testing

import (
	"reflect"
	stdtesting "testing"

	"github.com/Newton-School/gogo/conf"
)

func TestOverrideSettingsRestoresAppsMiddlewareDatabaseAndTemplates(t *stdtesting.T) {
	settings := conf.Settings{
		InstalledApps: []string{"base.app"},
		Middleware:    []string{"base.middleware"},
		DatabaseURL:   "postgres://original",
		TemplateDirs:  []string{"/templates/base"},
	}

	override := OverrideSettings(&settings,
		WithInstalledApps("gogo.contrib.sites", "blog"),
		WithMiddleware("gogo.http.RequestIDMiddleware"),
		WithDatabaseURL("sqlite:///tmp/test.sqlite3"),
		WithTemplateDirs("/tmp/templates", "/tmp/project/templates"),
	)

	if !reflect.DeepEqual(settings.InstalledApps, []string{"gogo.contrib.sites", "blog"}) {
		t.Fatalf("InstalledApps = %#v", settings.InstalledApps)
	}
	if !reflect.DeepEqual(settings.Middleware, []string{"gogo.http.RequestIDMiddleware"}) {
		t.Fatalf("Middleware = %#v", settings.Middleware)
	}
	if settings.DatabaseURL != "sqlite:///tmp/test.sqlite3" {
		t.Fatalf("DatabaseURL = %q", settings.DatabaseURL)
	}
	if !reflect.DeepEqual(settings.TemplateDirs, []string{"/tmp/templates", "/tmp/project/templates"}) {
		t.Fatalf("TemplateDirs = %#v", settings.TemplateDirs)
	}

	override.Restore()
	if !reflect.DeepEqual(settings.InstalledApps, []string{"base.app"}) ||
		!reflect.DeepEqual(settings.Middleware, []string{"base.middleware"}) ||
		settings.DatabaseURL != "postgres://original" ||
		!reflect.DeepEqual(settings.TemplateDirs, []string{"/templates/base"}) {
		t.Fatalf("settings after restore = %#v", settings)
	}
}

func TestOverrideSettingsForTestRegistersAutomaticRestore(t *stdtesting.T) {
	settings := conf.Settings{InstalledApps: []string{"base"}}
	cleanup := &cleanupRecorder{}

	OverrideSettingsForTest(cleanup, &settings, WithInstalledApps("temporary"))
	if !reflect.DeepEqual(settings.InstalledApps, []string{"temporary"}) {
		t.Fatalf("InstalledApps = %#v", settings.InstalledApps)
	}

	cleanup.run()
	if !reflect.DeepEqual(settings.InstalledApps, []string{"base"}) {
		t.Fatalf("InstalledApps after cleanup = %#v", settings.InstalledApps)
	}
}

func TestTemporarySettingsReturnsIsolatedCopy(t *stdtesting.T) {
	base := conf.Settings{
		InstalledApps: []string{"base"},
		Middleware:    []string{"middleware"},
		TemplateDirs:  []string{"templates"},
	}

	temporary := TemporarySettings(base, WithInstalledApps("copy"), WithMiddleware("m1", "m2"))
	temporary.InstalledApps[0] = "mutated"
	temporary.Middleware[0] = "changed"

	if !reflect.DeepEqual(base.InstalledApps, []string{"base"}) || !reflect.DeepEqual(base.Middleware, []string{"middleware"}) {
		t.Fatalf("base settings mutated = %#v", base)
	}
}

type cleanupRecorder struct {
	callbacks []func()
}

func (c *cleanupRecorder) Helper() {}

func (c *cleanupRecorder) Cleanup(callback func()) {
	c.callbacks = append(c.callbacks, callback)
}

func (c *cleanupRecorder) run() {
	for i := len(c.callbacks) - 1; i >= 0; i-- {
		c.callbacks[i]()
	}
}
