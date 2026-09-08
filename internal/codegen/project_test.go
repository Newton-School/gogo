package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectAppAndPreserveExisting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "storefront")
	if err := StartProject(dir, ProjectOptions{Module: "example.com/storefront"}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	example, _ := os.ReadFile(filepath.Join(dir, ".env.example"))
	if string(env) != string(example) {
		t.Fatal("env drift")
	}
	settings, err := os.ReadFile(filepath.Join(dir, "config/settings.go"))
	if err != nil {
		t.Fatal(err)
	}
	connections, err := os.ReadFile(filepath.Join(dir, "config/connections.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "management.InspectDBCommand(connections.Inspection)") || !strings.Contains(string(connections), "db.CatalogIntrospector") || !strings.Contains(string(connections), "c.Database.Alias() != alias") {
		t.Fatal("inspectdb lazy configured-alias registration missing")
	}
	if err := StartApp(dir, "catalog"); err != nil {
		t.Fatal(err)
	}
	apps, _ := os.ReadFile(filepath.Join(dir, "config/apps.go"))
	if !strings.Contains(string(apps), "catalog.App()") {
		t.Fatal(string(apps))
	}
	if err := StartApp(dir, "catalog"); err == nil {
		t.Fatal("duplicate app accepted")
	}
	if err := StartProject(dir, ProjectOptions{Module: "example.com/overwrite"}); err == nil {
		t.Fatal("overwrote project")
	}
}
func TestRejectUnsafeGeneration(t *testing.T) {
	for _, module := range []string{"", "../bad", "example.com/a/../b", "example.com/x\nreplace"} {
		if err := StartProject(filepath.Join(t.TempDir(), "x"), ProjectOptions{Module: module}); err == nil {
			t.Fatal(module)
		}
	}
	if err := StartApp(t.TempDir(), "../escape"); err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestValidateEntireTreeBeforeWriting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	err := writeTree(dir, map[string]string{"a.txt": "valid", "z.go": "not Go source"})
	if err == nil {
		t.Fatal("invalid source accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("partial output left behind: %v", err)
	}
}

func TestStartAppCannotEscapeProjectThroughSymlinks(t *testing.T) {
	for _, target := range []string{"apps", "config/apps.go"} {
		t.Run(target, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "project")
			if err := StartProject(dir, ProjectOptions{Module: "example.com/project"}); err != nil {
				t.Fatal(err)
			}
			external := t.TempDir()
			link := filepath.Join(dir, target)
			// Move only this test's generated fixture out of the link location.
			if err := os.Rename(link, link+".fixture"); err != nil {
				t.Fatal(err)
			}
			if target == "config/apps.go" {
				external = filepath.Join(external, "apps.go")
				b, err := os.ReadFile(link + ".fixture")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(external, b, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(external, link); err != nil {
				t.Fatal(err)
			}
			if err := StartApp(dir, "catalog"); err == nil {
				t.Fatal("symlink escape accepted")
			}
			if target == "apps" {
				entries, err := os.ReadDir(external)
				if err != nil || len(entries) != 0 {
					t.Fatalf("wrote outside project: %v", err)
				}
			}
		})
	}
}
