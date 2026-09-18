package integration_test

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/db"
)

// Exercise the exact documented client files against a marked disposable
// cluster, never an application DATABASE_URL. This does not publish a release.
func TestDocumentationStorefrontPostgres(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var socket, port, database, schema string
	if err := db.QueryRow(ctx, backend,
		"SELECT current_setting('unix_socket_directories'), current_setting('port'), current_database(), current_schema()",
		nil, &socket, &port, &database, &schema); err != nil {
		t.Fatal("read owned database coordinates")
	}
	if !filepath.IsAbs(socket) || strings.Contains(socket, ",") || !strings.HasPrefix(schema, "gogo_test_") {
		t.Fatal("documentation test requires the owned Unix-socket integration fixture")
	}
	dsn := &url.URL{Scheme: "postgres", User: url.User("gogo_test"), Host: "localhost", Path: "/" + database}
	query := url.Values{"host": {socket}, "port": {port}, "sslmode": {"disable"}, "search_path": {schema}}
	dsn.RawQuery = query.Encode()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(t.TempDir(), "storefront")
	var environment []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "GOGO_") || key == "DOCS_DATABASE_URL" {
			continue
		}
		switch key {
		case "GOENV", "GOWORK", "GOFLAGS", "GOOS", "GOARCH", "GOROOT", "GOTOOLCHAIN":
			continue
		}
		environment = append(environment, item)
	}
	environment = append(environment, "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod",
		"GOGO_DATABASE_URL="+dsn.String(), "DOCS_DATABASE_URL="+dsn.String())
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	run := func(directory string, args ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, goBinary, args...)
		command.Dir, command.Env = directory, environment
		if output, err := command.CombinedOutput(); err != nil {
			// Coordinates are owned but still keep connection values out of reports.
			safe := strings.ReplaceAll(string(output), dsn.String(), "[database URL redacted]")
			t.Fatalf("documentation command %v: %v\n%s", args, err, safe)
		}
	}
	run(root, "run", "./cmd/gogo", "startproject", client, "--module", "example.com/storefront")
	run(client, "mod", "edit", "-replace=github.com/Newton-School/gogo="+root,
		"-replace=github.com/Newton-School/gogo/connectors/postgres="+filepath.Join(root, "connectors/postgres"))
	run(client, "mod", "tidy")
	run(client, "run", "manage.go", "startapp", "catalog")
	copyFile := func(source, destination string) error {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0600)
	}
	for _, name := range []string{"models.go", "urls.go"} {
		if err := copyFile(filepath.Join(root, "docs/snippets/catalog", name), filepath.Join(client, "apps/catalog", name)); err != nil {
			t.Fatal(err)
		}
	}
	snippets := filepath.Join(root, "docs/snippets/storefront")
	if err := filepath.WalkDir(snippets, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go.txt") {
			return nil
		}
		relative, err := filepath.Rel(snippets, path)
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(client, strings.TrimSuffix(relative, ".txt")))
	}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"run", "manage.go", "generate"},
		{"run", "manage.go", "makemigrations", "catalog"},
		{"run", "manage.go", "migrate"},
		{"run", "manage.go", "seed"},
		{"run", "manage.go", "seed"},
		{"test", "-race", "-count=1", "./..."},
		{"build", "-o", filepath.Join(t.TempDir(), "manage"), "."},
	} {
		run(client, args...)
	}
}
