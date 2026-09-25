package integration_test

import (
	"context"
	"fmt"
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

func TestRunScriptShowcasePostgres(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var socket, port, database, schema string
	if err := db.QueryRow(ctx, backend,
		"SELECT current_setting('unix_socket_directories'), current_setting('port'), current_database(), current_schema()",
		nil, &socket, &port, &database, &schema); err != nil {
		t.Fatal("read owned database coordinates")
	}
	if !filepath.IsAbs(socket) || strings.Contains(socket, ",") || !strings.HasPrefix(schema, "gogo_test_") {
		t.Fatal("script test requires the owned Unix-socket integration fixture")
	}
	// Only the fixture schema is populated, never the developer's showcase DB.
	if _, err := backend.Exec(ctx, `CREATE TABLE catalog_product (
 id bigint PRIMARY KEY, name text, slug text, description text,
 price numeric(12,2), stock bigint, published boolean, created_at timestamptz
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Exec(ctx, "INSERT INTO catalog_product (id) VALUES (1), (2)"); err != nil {
		t.Fatal(err)
	}
	dsn := &url.URL{Scheme: "postgres", User: url.User("gogo_test"), Host: "localhost", Path: "/" + database}
	dsn.RawQuery = url.Values{"host": {socket}, "port": {port}, "sslmode": {"disable"}}.Encode()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	client := t.TempDir()
	source := filepath.Join(root, "examples/showcase")
	// Copy only public source/assets. In particular, no local .env is read by
	// the new manage process or its compiled script.
	for _, directory := range []string{"config", "apps", "scripts"} {
		if err := os.CopyFS(filepath.Join(client, directory), os.DirFS(filepath.Join(source, directory))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "manage.go"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			for _, module := range []string{"", "/admin", "/async", "/async/redis", "/connectors/postgres", "/connectors/redis"} {
				data = append(data, fmt.Appendf(nil, "\nreplace github.com/Newton-School/gogo%s => %q\n", module, filepath.ToSlash(root+module))...)
			}
		}
		if err = os.WriteFile(filepath.Join(client, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Existing helper removes harness GOGO_* names and fixes builds to local
	// modules. Only the new fixture's database URL is given to the script.
	environment := append(openAPICommandEnvironment(os.Environ()),
		"GOGO_DATABASE_URL="+dsn.String(), "GOGO_SHOWCASE_SCHEMA="+schema)
	run := func(executable string, args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, executable, args...)
		command.Dir, command.Env = client, environment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("script command failed: %v\n%s", err, strings.ReplaceAll(string(output), dsn.String(), "[database URL redacted]"))
		}
		return string(output)
	}
	manage := filepath.Join(client, "manage")
	if runtime.GOOS == "windows" {
		manage += ".exe"
	}
	run(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", manage, "manage.go")
	if output := run(manage, "runscript", "scripts/inspect/main.go", "--", "demo"); !strings.Contains(output, "Registered models: 14\nArguments: [\"demo\"]") {
		t.Fatal(output)
	}
	if output := run(manage, "runscript", "scripts/catalog-report/main.go"); output != "Products: 2\n" {
		t.Fatal(output)
	}
	var remaining int64
	if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM catalog_product", nil, &remaining); err != nil || remaining != 2 {
		t.Fatal("report modified records", err, remaining)
	}
}
