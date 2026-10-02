package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartServicePreservesProjectAndHasNoImplicitResources(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := StartProject(dir, ProjectOptions{Module: "example.com/backend"}); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, name := range []string{"manage.go", "go.mod", "config/apps.go", "config/settings.go", ".env", ".env.example"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(data)
	}
	for _, name := range []string{"sessions", "billing-worker"} {
		if err := StartService(dir, name); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "services", name, "project.go"))
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		if !strings.Contains(source, `"./services/`+name+`"`) || strings.Contains(source, "RuntimeResources:") || strings.Contains(source, "example.com/backend/config") {
			t.Fatal(source)
		}
		if err := StartService(dir, name); err == nil {
			t.Fatal("overwrote service")
		}
	}
	for name, want := range before {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("changed %s: %v", name, err)
		}
	}
}

func TestStartServiceRejectsUnsafeDestinations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := StartProject(dir, ProjectOptions{Module: "example.com/backend"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../outside", "/tmp/service", ".hidden", "../", "api/server", "api\\server", "--api", "api\n", "Upper", "con", "lpt1", strings.Repeat("a", 64)} {
		if err := StartService(dir, name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(dir, "services", "outside")); err != nil {
		t.Fatal(err)
	}
	if err := StartService(dir, "outside"); err == nil {
		t.Fatal("accepted symlink service")
	}
	if err := os.Rename(filepath.Join(dir, "services"), filepath.Join(dir, "services-backup")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dir, "services")); err != nil {
		t.Fatal(err)
	}
	if err := StartService(dir, "api"); err == nil {
		t.Fatal("accepted symlink parent")
	}
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatalf("modified external directory: %v", err)
	}
	if err := StartService(t.TempDir(), "api"); err == nil {
		t.Fatal("accepted non-project")
	}
}
