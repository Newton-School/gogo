package management

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceScaffoldingCommand(t *testing.T) {
	isolateCommandEnvironment(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/backend\ngo 1.26.8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	project := Project{Root: dir, ResourceFactory: nil}
	options := Options{Stdout: io.Discard, Stderr: io.Discard}
	for _, args := range [][]string{{"startservice"}, {"startservice", "a", "b"}, {"startservice", "../outside"}} {
		if err := Call(context.Background(), project, args, options); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := Call(context.Background(), project, []string{"startservice", "sessions"}, options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "services/sessions/main.go")); err != nil {
		t.Fatal(err)
	}
}

func TestReloadMainPackage(t *testing.T) {
	for name, want := range map[string]string{"": "manage.go", ".": ".", "./services/api": "./services/api", "./services/billing-worker": "./services/billing-worker"} {
		got, err := reloadMainPackage(name)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", name, got, err)
		}
	}
	for _, name := range []string{"manage.go", "-o", "./services/...", "./../escape", "../escape", "/tmp/main", "example.com/main", "./services//api", "./services/api/main.go", "./services/../api", "./services/.hidden", "./services/api\n", "./services/api;echo"} {
		if _, err := reloadMainPackage(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
		if _, _, err := captureRunServerProject(Project{MainPackage: name}); err == nil {
			t.Fatalf("entry accepted %q", name)
		}
	}
}
