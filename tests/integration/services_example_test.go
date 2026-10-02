//go:build darwin || linux

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/db"
)

func TestIndependentServicesShareDatabaseHTTPAndTasks(t *testing.T) {
	backend := testservice.Postgres(t)
	redis := fixture.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var socket, port, database, schema string
	if err := db.QueryRow(ctx, backend, "SELECT current_setting('unix_socket_directories'), current_setting('port'), current_database(), current_schema()", nil, &socket, &port, &database, &schema); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(socket) || strings.Contains(socket, ",") || !strings.HasPrefix(schema, "gogo_test_") {
		t.Fatal("requires owned Unix-socket fixture")
	}
	dsn := &url.URL{Scheme: "postgres", User: url.User("gogo_test"), Host: "localhost", Path: "/" + database}
	dsn.RawQuery = url.Values{"host": {socket}, "port": {port}, "sslmode": {"disable"}, "search_path": {schema}}.Encode()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	client := t.TempDir()
	for _, directory := range []string{"config", "internal", "apps", "services"} {
		if err := os.CopyFS(filepath.Join(client, directory), os.DirFS(filepath.Join(root, "examples/services", directory))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"manage.go", "go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, "examples/services", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			for _, module := range []string{"", "/async", "/async/redis", "/connectors/postgres", "/connectors/redis"} {
				data = append(data, fmt.Appendf(nil, "\nreplace github.com/Newton-School/gogo%s => %q\n", module, filepath.ToSlash(root+module))...)
			}
		}
		if err := os.WriteFile(filepath.Join(client, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	environment := openAPICommandEnvironment(os.Environ())
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	h := &reloadHarness{t: t, root: client, environment: environment, client: &http.Client{Transport: transport}}
	goBinary := filepath.Join(runtime.GOROOT(), "bin/go")
	paths := map[string]string{}
	for name, target := range map[string]string{"manage": ".", "api": "./services/api", "reports": "./services/reports", "worker": "./services/worker"} {
		paths[name] = filepath.Join(t.TempDir(), name)
		h.run(client, goBinary, "build", "-o", paths[name], target)
	}
	if deps := h.run(client, goBinary, "list", "-deps", "./services/worker"); strings.Contains(deps, "gogo/connectors/postgres") || strings.Contains(deps, "gogo-services/apps/catalog") {
		t.Fatal("worker linked database or catalog app")
	}
	if deps := h.run(client, goBinary, "list", "-deps", "./services/api"); strings.Contains(deps, "gogo/async") || strings.Contains(deps, "gogo/connectors/redis") {
		t.Fatal("API linked queue dependencies")
	}
	dbEnv := append(append([]string(nil), environment...), "GOGO_DATABASE_URL="+dsn.String())
	queueEnv := append(append([]string(nil), environment...), "GOGO_REDIS_URL="+redis.URL)
	h.environment = dbEnv
	h.run(client, paths["manage"], "migrate")
	h.run(client, paths["manage"], "seed")
	h.run(client, paths["manage"], "seed")
	freeAddress := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		return address
	}
	apiAddress, reportAddress := freeAddress(), freeAddress()
	api := startReloadProcess(t, client, dbEnv, paths["api"], "serve", "--addr="+apiAddress)
	reports := startReloadProcess(t, client, dbEnv, paths["reports"], "serve", "--addr="+reportAddress)
	worker := startReloadProcess(t, client, queueEnv, paths["worker"], "worker")
	for _, address := range []string{apiAddress, reportAddress} {
		h.wait("public service count", func() bool {
			status, body, err := h.response(address, "/products/count/")
			return err == nil && status == 200 && strings.Contains(string(body), `"count":2`)
		})
		if status, _, err := h.response(address, "/admin/"); err != nil || status != 404 {
			t.Fatal("service exposed Admin", status, err)
		}
	}
	h.environment = append(append([]string(nil), dbEnv...), "GOGO_REDIS_URL="+redis.URL)
	output := h.run(client, paths["reports"], "report", "--api-url=http://"+apiAddress)
	for _, want := range []string{`"database_count":2`, `"api_count":2`, `"task_id":`, `"doubled_count":4`} {
		if !strings.Contains(output, want) {
			t.Fatalf("report missing %s: %s", want, output)
		}
	}
	// One process stopping must not stop the other independently owned server.
	api.stop(t, false)
	if status, body, err := h.response(reportAddress, "/products/count/"); err != nil || status != 200 || !strings.Contains(string(body), `"count":2`) {
		t.Fatal("reports depended on API process lifetime", status, err)
	}
	reports.stop(t, false)
	worker.stop(t, false)
}
