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
