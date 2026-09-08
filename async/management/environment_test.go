package management_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/async"
	commands "github.com/Newton-School/gogo/async/management"
	fakes "github.com/Newton-School/gogo/async/testing"
	core "github.com/Newton-School/gogo/core/management"
)

// Management fixtures exercise explicitly constructed projects, not the parent
// process's live configuration or integration-service flags. Setenv registers
// restoration and prevents parallel use; Unsetenv then removes the key entirely
// because even an empty unknown GOGO_ setting is rejected by the strict schema.
func isolateCommandEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCommandFixtureIsolatesAndRestoresAmbientConfiguration(t *testing.T) {
	values := map[string]string{
		"GOGO_TEST_POSTGRES_DSN":     "unused-test-service-setting",
		"GOGO_TEST_REQUIRE_SERVICES": "1",
		"GOGO_DB_MAX_OPEN":           "invalid-live-project-setting",
		"GOGO_TEST_EMPTY":            "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	const unrelated = "ASYNC_MANAGEMENT_FIXTURE_UNRELATED"
	t.Setenv(unrelated, "unchanged")
	t.Run("isolated command", func(t *testing.T) {
		isolateCommandEnvironment(t)
		for key := range values {
			if _, exists := os.LookupEnv(key); exists {
				t.Fatalf("fixture retained ambient key %s", key)
			}
		}
		if os.Getenv(unrelated) != "unchanged" {
			t.Fatal("fixture changed an unrelated variable")
		}
		backend := fakes.NewMemory()
		client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Presence: backend})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		project := core.Project{
			Root:        t.TempDir(),
			Environment: map[string]string{"GOGO_DB_MAX_OPEN": "7", "GOGO_DB_MAX_IDLE": "2"},
			Commands: commands.Commands(commands.Factories{Client: func(_ context.Context, invocation *core.Invocation) (*async.Client, error) {
				calls++
				if invocation.Settings.Int("GOGO_DB_MAX_OPEN") != 7 || invocation.Settings.Int("GOGO_DB_MAX_IDLE") != 2 {
					t.Fatal("explicit fixture configuration was not preserved")
				}
				return client, nil
			}}),
		}
		var output bytes.Buffer
		if err := core.Call(context.Background(), project, []string{"tasks", "inspect", "workers", "missing"}, core.Options{Stdout: &output, Stderr: &output}); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !strings.Contains(output.String(), `"status":"unknown"`) {
			t.Fatal("isolated command did not execute", calls, output.String())
		}
	})
	for key, want := range values {
		if got, exists := os.LookupEnv(key); !exists || got != want {
			t.Fatalf("fixture did not restore %s", key)
		}
	}
	if os.Getenv(unrelated) != "unchanged" {
		t.Fatal("fixture changed an unrelated variable after cleanup")
	}
}
