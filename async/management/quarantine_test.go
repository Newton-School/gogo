package management_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/async"
	commands "github.com/Newton-School/gogo/async/management"
	fakes "github.com/Newton-School/gogo/async/testing"
	core "github.com/Newton-School/gogo/core/management"
)

func TestQuarantineCommandListsOnlyAuthorizedRedactedPage(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.quarantine_cli", 1, func(_ context.Context, _ async.TaskContext, v string) (string, error) { return v, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	deny := false
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Authorize: func(_ context.Context, action, scope, _ string) error {
		if action == "inspect_quarantine" && (deny || scope != "default") {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.Delay(ctx, client, "private-cli-argument"); err != nil {
		t.Fatal(err)
	}
	d, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Reject(ctx, d, "unknown_task", false); err != nil {
		t.Fatal(err)
	}
	factories := 0
	project := core.Project{Root: t.TempDir(), Commands: commands.Commands(commands.Factories{Client: func(context.Context, *core.Invocation) (*async.Client, error) { factories++; return client, nil }})}
	var output bytes.Buffer
	options := core.Options{Stdout: &output, Stderr: &output}
	if err := core.Call(ctx, project, []string{"queues", "quarantine", "default", "-", "1"}, options); err != nil {
		t.Fatal(err)
	}
	var page async.QuarantinePage
	if err := json.Unmarshal(output.Bytes(), &page); err != nil || len(page.Entries) != 1 || strings.Contains(output.String(), "private-cli-argument") {
		t.Fatal(output.String(), err)
	}
	output.Reset()
	if err := core.Call(ctx, project, []string{"queues", "quarantine", "default", page.NextCursor, "1"}, options); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &page); err != nil || len(page.Entries) != 0 {
		t.Fatal(page, err)
	}
	deny = true
	output.Reset()
	if err := core.Call(ctx, project, []string{"queues", "quarantine", "default"}, options); !errors.Is(err, async.ErrDenied) || output.Len() != 0 {
		t.Fatal(output.String(), err)
	}
	for _, args := range [][]string{{"queues", "quarantine"}, {"queues", "quarantine", "*"}, {"queues", "quarantine", "default", "-", "0"}, {"queues", "quarantine", "default", "-", "1001"}, {"queues", "quarantine", "default", "-", "01"}, {"queues", "replay", "default"}} {
		before := factories
		if err := core.Call(ctx, project, args, options); err == nil || factories != before {
			t.Fatal("invalid control opened client", args, err)
		}
	}
}
