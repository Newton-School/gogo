package integration_test

import (
	"context"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoreTasksExternalConsumerWithoutAsync(t *testing.T) {
	// This is a source-linked external client, not a packaged installation.
	// The runtime provider is an explicit test double; actual Async/Redis bridge
	// execution is covered by the separate real-provider conformance tests.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	checksums, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	module := fmt.Sprintf("module example.com/core-task-client\n\ngo 1.26.8\n\nrequire github.com/Newton-School/gogo v0.0.0\n\nreplace github.com/Newton-School/gogo => %q\n", root)
	files := map[string][]byte{"go.mod": []byte(module), "go.sum": checksums, "client.go": []byte(coreTaskConsumer), "client_test.go": []byte(coreTaskConsumerTest)}
	for name, value := range files {
		if strings.HasSuffix(name, ".go") {
			value, err = format.Source(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(directory, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Reuse the checksum-enabled, workspace-disabled child environment helper;
	// no ambient GOGO_* integration setting becomes application configuration.
	environment := openAPICommandEnvironment(os.Environ())
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir, command.Env = directory, environment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("Core-only consumer %v: %v\n%s", args, err, output)
		}
		return string(output)
	}
	run("test", "-race", "-count=1", "./...")
	dependencies := run("list", "-deps", "-test", "-f", "{{.ImportPath}}", "./...")
	if !strings.Contains(dependencies, "github.com/Newton-School/gogo/core/tasks\n") {
		t.Fatal("consumer did not exercise the public Core port")
	}
	for _, line := range strings.Split(dependencies, "\n") {
		for _, forbidden := range []string{"github.com/Newton-School/gogo/async", "github.com/Newton-School/gogo/admin", "github.com/Newton-School/gogo/connectors"} {
			if line == forbidden || strings.HasPrefix(line, forbidden+"/") {
				t.Fatal("Core client imports an optional runtime", line)
			}
		}
	}
	modules := run("list", "-m", "all")
	for _, forbidden := range []string{"github.com/Newton-School/gogo/async", "github.com/Newton-School/gogo/admin", "github.com/Newton-School/gogo/connectors"} {
		if strings.Contains(modules, forbidden) {
			t.Fatal("Core client requires an optional module", forbidden)
		}
	}
}

const coreTaskConsumer = `package client

import (
    "context"
    "encoding/json"
    "github.com/Newton-School/gogo/core/tasks"
)

// Dispatch depends only on Core. Production composition supplies its provider;
// this library neither discovers one nor falls back to inline execution.
func Dispatch(ctx context.Context, provider tasks.Dispatcher) (tasks.Result, error) {
    if provider == nil { return nil, tasks.ErrConfiguration }
    return provider.Enqueue(ctx, tasks.Request{
        Task: "reports.total", Version: 1, Scope: "reports",
        Args: json.RawMessage("123456789012345678901234567890"),
    })
}
`

const coreTaskConsumerTest = `package client_test

import (
    "context"
    "encoding/json"
    "errors"
    "testing"
    client "example.com/core-task-client"
    "github.com/Newton-School/gogo/core/tasks"
)

// Explicit test fixture only; no production eager backend or worker is implied.
type fixture struct { request tasks.Request }
type result struct { id string; value json.RawMessage }
func (f *fixture) Enqueue(_ context.Context, request tasks.Request) (tasks.Result, error) {
    f.request = request
    return result{"00000000-0000-4000-8000-000000000001", append(json.RawMessage(nil), request.Args...)}, nil
}
func (r result) ID() string { return r.id }
func (r result) Get(context.Context) (json.RawMessage, error) { return append(json.RawMessage(nil), r.value...), nil }

func TestCoreOnlyDispatch(t *testing.T) {
    ctx := context.Background()
    if handle, err := client.Dispatch(ctx, nil); handle != nil || !errors.Is(err, tasks.ErrConfiguration) { t.Fatal("missing provider enabled fallback") }
    provider := &fixture{}
    handle, err := client.Dispatch(ctx, provider)
    if err != nil || handle.ID() == "" || provider.request.Scope != "reports" || provider.request.Version != 1 { t.Fatal("lost portable request") }
    value, err := handle.Get(ctx)
    if err != nil || string(value) != "123456789012345678901234567890" { t.Fatal("result changed") }
    cause := errors.New("local provider cause")
    admission := &tasks.AcceptanceError{ID: handle.ID(), Confirmed: true, Cause: cause}
    if !errors.Is(admission, cause) || admission.ID != handle.ID() || !admission.Confirmed { t.Fatal("acceptance identity lost") }
}
`
