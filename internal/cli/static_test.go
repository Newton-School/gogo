package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/static"
)

func TestCollectstaticCommandRunsConfiguredCollector(t *testing.T) {
	var captured static.CollectOptions
	command := NewCollectstaticCommand(func(_ context.Context, options static.CollectOptions) (static.CollectResult, error) {
		captured = options
		return static.CollectResult{Copied: []static.CollectedFile{{SourcePath: "css/app.css"}, {SourcePath: "js/app.js"}}}, nil
	})

	var stdout bytes.Buffer
	err := command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{
		"--dest", "public/static",
		"--project-dir", "assets",
		"--app-dir", "blog/static",
		"--manifest",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runWithIO() error = %v", err)
	}
	if captured.Destination != "public/static" || !captured.Manifest {
		t.Fatalf("captured options = %#v", captured)
	}
	if !reflect.DeepEqual(captured.Finder.ProjectDirs, []string{"assets"}) || !reflect.DeepEqual(captured.Finder.AppDirs, []string{"blog/static"}) {
		t.Fatalf("captured finder = %#v", captured.Finder)
	}
	if stdout.String() != "collected 2 static files\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestCollectstaticCommandSupportsDryRun(t *testing.T) {
	var captured static.CollectOptions
	command := NewCollectstaticCommand(func(_ context.Context, options static.CollectOptions) (static.CollectResult, error) {
		captured = options
		return static.CollectResult{Copied: []static.CollectedFile{{SourcePath: "css/app.css"}}}, nil
	})

	var stdout bytes.Buffer
	err := command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{
		"--dest", "public/static",
		"--dry-run",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runWithIO() error = %v", err)
	}
	if !captured.DryRun {
		t.Fatalf("DryRun = false, want true")
	}
	if stdout.String() != "would collect 1 static files\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestCollectstaticDefaultsFromGeneratedProject(t *testing.T) {
	dir := t.TempDir()
	writeTextFile(t, filepath.Join(dir, ".env"), `
GOGO_STATIC_ROOT=staticfiles
`)
	if err := os.MkdirAll(filepath.Join(dir, "static", "css"), 0o755); err != nil {
		t.Fatalf("mkdir project static: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "apps", "blog", "static", "blog"), 0o755); err != nil {
		t.Fatalf("mkdir app static: %v", err)
	}
	writeTextFile(t, filepath.Join(dir, "static", "css", "site.css"), "body{}\n")
	writeTextFile(t, filepath.Join(dir, "apps", "blog", "static", "blog", "app.css"), ".blog{}\n")

	var captured static.CollectOptions
	command := NewCollectstaticCommand(func(_ context.Context, options static.CollectOptions) (static.CollectResult, error) {
		captured = options
		return static.CollectResult{Copied: []static.CollectedFile{{SourcePath: "css/site.css"}}}, nil
	})

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	var stdout bytes.Buffer
	err = command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), nil, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runWithIO() error = %v", err)
	}
	if captured.Destination != "staticfiles" {
		t.Fatalf("Destination = %q, want staticfiles", captured.Destination)
	}
	if !reflect.DeepEqual(captured.Finder.ProjectDirs, []string{"static"}) {
		t.Fatalf("ProjectDirs = %#v, want static", captured.Finder.ProjectDirs)
	}
	if !reflect.DeepEqual(captured.Finder.AppDirs, []string{filepath.Join("apps", "blog", "static")}) {
		t.Fatalf("AppDirs = %#v, want blog static", captured.Finder.AppDirs)
	}
}
