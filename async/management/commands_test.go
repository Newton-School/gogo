package management_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	commands "github.com/Newton-School/gogo/async/management"
	fakes "github.com/Newton-School/gogo/async/testing"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	core "github.com/Newton-School/gogo/core/management"
)

func TestManagementWorkerTaskStatusAndScopedRevoke(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.management", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend})
	if err != nil {
		t.Fatal(err)
	}
	project := core.Project{Root: t.TempDir(), Commands: commands.Commands(commands.Factories{
		Worker: func(context.Context, *core.Invocation) (commands.WorkerRuntime, error) {
			return commands.WorkerRuntime{Worker: &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "managed"}}, nil
		},
		Client: func(context.Context, *core.Invocation) (*async.Client, error) { return client, nil },
	})}
	result, err := task.Delay(ctx, client, 41)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := core.Options{Stdout: &output, Stderr: &output}
	if err := core.Call(ctx, project, []string{"worker", "--once", "--concurrency", "2", "--queues", "default"}, options); err != nil {
		t.Fatal(err)
	}
	if err := core.Call(ctx, project, []string{"tasks", "result", result.Receipt.ID}, options); err != nil || output.String() != "42\n" {
		t.Fatal(output.String(), err)
	}
	output.Reset()
	if err := core.Call(ctx, project, []string{"tasks", "revoke", result.Receipt.ID}, options); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"cancellation_requested":false`)) || !bytes.Contains(output.Bytes(), []byte(`"state":"SUCCEEDED"`)) {
		t.Fatal("claimed completed task was canceled", output.String())
	}
	output.Reset()
	if err := core.Call(ctx, project, []string{"tasks", "status", result.Receipt.ID}, options); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("args")) || bytes.Contains(output.Bytes(), []byte("headers")) || bytes.Contains(output.Bytes(), []byte("principal")) {
		t.Fatal("status exposed task metadata", output.String())
	}
	denied, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Authorize: func(context.Context, string, string, string) error { return async.ErrDenied }})
	if err != nil {
		t.Fatal(err)
	}
	project.Commands = commands.Commands(commands.Factories{Client: func(context.Context, *core.Invocation) (*async.Client, error) { return denied, nil }})
	output.Reset()
	if err := core.Call(ctx, project, []string{"tasks", "status", result.Receipt.ID}, options); !errors.Is(err, async.ErrDenied) || output.Len() != 0 {
		t.Fatal(output.String(), err)
	}
}

func TestManagementInvalidArgumentsBeforeAnyResourceOrFactory(t *testing.T) {
	opened, created := false, false
	project := core.Project{Root: t.TempDir(), ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { opened = true; return nil, nil }, Commands: commands.Commands(commands.Factories{
		Worker: func(context.Context, *core.Invocation) (commands.WorkerRuntime, error) {
			created = true
			return commands.WorkerRuntime{}, nil
		},
		Client: func(context.Context, *core.Invocation) (*async.Client, error) { created = true; return nil, nil },
	})}
	for _, args := range [][]string{{"worker", "--concurrency", "0"}, {"worker", "--queues", "*"}, {"worker", "extra"}, {"tasks", "retry", "invalid"}, {"queues", "purge", "default"}} {
		var output bytes.Buffer
		err := core.Call(context.Background(), project, args, core.Options{Stdout: &output, Stderr: &output})
		var commandErr *core.CommandError
		if !errors.As(err, &commandErr) || commandErr.Code != 2 || opened || created {
			t.Fatal(args, err, opened, created)
		}
	}
}

func TestManagementChildUsesSameDeclaredRegistry(t *testing.T) {
	registry := async.NewRegistry()
	_, err := async.Register(registry, "test.child", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := async.NewID()
	max := 3
	execution := async.Execution{Envelope: async.Envelope{ProtocolVersion: 1, ID: id, Task: "test.child", Version: 1, Args: json.RawMessage(`41`), CreatedAt: time.Now(), Queue: "default", MaxRetries: &max}, TaskContext: async.TaskContext{ID: id}}
	payload, _ := json.Marshal(execution)
	project := core.Project{Root: t.TempDir(), Commands: commands.Commands(commands.Factories{Child: func(context.Context, *core.Invocation) (*async.Registry, error) { return registry, nil }})}
	var output bytes.Buffer
	if err := core.Call(context.Background(), project, []string{"task-child"}, core.Options{Stdin: bytes.NewReader(payload), Stdout: &output, Stderr: &output}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || string(response.Output) != "42" {
		t.Fatal(output.String(), err)
	}
}

func TestRevokeCommandRequiresReadGrantBeforeMutation(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.revoke_only", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Authorize: func(_ context.Context, operation, _, _ string) error {
		if operation == "read" {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	project := core.Project{Root: t.TempDir(), Commands: commands.Commands(commands.Factories{Client: func(context.Context, *core.Invocation) (*async.Client, error) { return client, nil }})}
	var output bytes.Buffer
	if err := core.Call(ctx, project, []string{"tasks", "revoke", result.Receipt.ID}, core.Options{Stdout: &output, Stderr: &output}); !errors.Is(err, async.ErrDenied) {
		t.Fatal(err)
	}
	record, err := backend.Lookup(ctx, result.Receipt.ID)
	if err != nil || record.CancelRequested || output.Len() != 0 {
		t.Fatal("failed command changed cancellation", record, output.String(), err)
	}
}
