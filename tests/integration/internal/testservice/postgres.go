// Package testservice starts disposable local services for cross-module tests.
package testservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/jackc/pgx/v5"
)

// Postgres allocates a random schema in a marked, local integration cluster.
// Without GOGO_TEST_POSTGRES_DSN it starts its own Unix-socket-only cluster using
// PATH, or the optional GOGO_TEST_POSTGRES_BIN directory. No application alias
// or DATABASE_URL is consulted. Cleanup verifies ownership before dropping DDL.
func Postgres(t *testing.T) *postgres.Backend {
	t.Helper()
	dsn := os.Getenv("GOGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = startPostgres(t)
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil || parsed.User != "gogo_test" || !(filepath.IsAbs(parsed.Host) || parsed.Host == "127.0.0.1" || parsed.Host == "::1") {
		t.Fatal("integration database must be a local, isolated gogo_test cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxOpen: 4, MaxIdle: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	var directory, role string
	if err := db.QueryRow(ctx, owner, "SELECT current_setting('data_directory'), current_user", nil, &directory, &role); err != nil {
		t.Fatal(err)
	}
	if role != "gogo_test" || !strings.HasPrefix(filepath.Base(filepath.Dir(directory)), "gogo-postgres.") || filepath.Base(directory) != "data" {
		t.Fatal("refusing DDL outside the marked disposable PostgreSQL cluster")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	marker := "gogo-integration-" + hex.EncodeToString(random[:])
	schema := "gogo_test_" + hex.EncodeToString(random[:])
	quoted, err := owner.Dialect().QuoteIdentifier(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "COMMENT ON SCHEMA "+quoted+" IS '"+marker+"'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var actual, actualOwner string
		if err := db.QueryRow(ctx, owner, "SELECT obj_description(oid, 'pg_namespace'), pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=$1", []any{schema}, &actual, &actualOwner); err != nil || actual != marker || actualOwner != "gogo_test" {
			t.Errorf("refusing cleanup: schema ownership marker changed: %v", err)
			return
		}
		if _, err := owner.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("cleanup isolated schema: %v", err)
		}
	})
	backend, err := postgres.Open(ctx, postgres.Config{DSN: dsn, SearchPath: schema, MaxOpen: 10, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func startPostgres(t *testing.T) string {
	t.Helper()
	unavailable := func(message string) {
		t.Helper()
		if os.Getenv("GOGO_TEST_REQUIRE_SERVICES") == "1" {
			t.Fatal(message)
		}
		t.Skip(message)
	}
	find := func(name string) string {
		path := name
		if directory := os.Getenv("GOGO_TEST_POSTGRES_BIN"); directory != "" {
			path = filepath.Join(directory, name)
		}
		resolved, err := exec.LookPath(path)
		if err != nil {
			unavailable("PostgreSQL server binaries unavailable; set GOGO_TEST_POSTGRES_BIN or PATH to run real integration tests")
		}
		return resolved
	}
	initdb, pgctl, server := find("initdb"), find("pg_ctl"), find("postgres")
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	var major int
	for _, binary := range []string{initdb, pgctl, server} {
		output, err := exec.CommandContext(versionCtx, binary, "--version").CombinedOutput()
		parts := regexp.MustCompile(`\(PostgreSQL\) ([0-9]+)`).FindStringSubmatch(string(output))
		if err != nil || len(parts) != 2 {
			t.Fatalf("cannot identify PostgreSQL binary version: %v", err)
		}
		version, _ := strconv.Atoi(parts[1])
		if version < 16 {
			unavailable("PostgreSQL 16+ required; set GOGO_TEST_POSTGRES_BIN to a supported server binary directory")
		}
		if major != 0 && major != version {
			t.Fatal("PostgreSQL server tooling has mixed major versions")
		}
		major = version
	}
	directory, err := os.MkdirTemp("", "gogo-postgres.")
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(directory, "data")
	started := false
	t.Cleanup(func() {
		if started {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if output, err := exec.CommandContext(ctx, pgctl, "-D", data, "-m", "fast", "-w", "stop").CombinedOutput(); err != nil {
				t.Errorf("stop owned PostgreSQL cluster: %v: %s", err, output)
				return // Preserve a possibly running cluster; never unlink its data.
			}
		}
		if filepath.Base(directory) == "." || !strings.HasPrefix(filepath.Base(directory), "gogo-postgres.") {
			t.Error("invalid cleanup directory")
			return
		}
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove owned cluster directory: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, initdb, "-D", data, "-U", "gogo_test", "-A", "trust", "--no-locale", "--encoding=UTF8").CombinedOutput(); err != nil {
		t.Fatalf("initialize isolated PostgreSQL: %v: %s", err, output)
	}
	// pg_ctl parses -o through a shell; quote the generated path, including any
	// apostrophe in the OS temporary-directory setting. TCP is disabled entirely.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	options := "-h '' -k " + quote(directory) + " -p 55439"
	if output, err := exec.CommandContext(ctx, pgctl, "-D", data, "-p", server, "-l", filepath.Join(directory, "server.log"), "-o", options, "-w", "start").CombinedOutput(); err != nil {
		t.Fatalf("start isolated PostgreSQL: %v: %s", err, output)
	}
	started = true
	// pgx keyword strings use backslash escapes rather than shell quoting.
	host := strings.ReplaceAll(strings.ReplaceAll(directory, "\\", "\\\\"), "'", "\\'")
	return "host='" + host + "' port=55439 user=gogo_test dbname=postgres sslmode=disable"
}
