package management_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	commands "github.com/Newton-School/gogo/async/management"
	fakes "github.com/Newton-School/gogo/async/testing"
	core "github.com/Newton-School/gogo/core/management"
)

func TestManagementInspectsWorkerPresenceAndMissingTargets(t *testing.T) {
	ctx := context.Background()
	backend := fakes.NewMemory()
	token, _ := async.NewID()
	lease := async.WorkerLease{WorkerID: "worker", Token: token}
	snapshot := async.WorkerSnapshot{ID: "worker", Queues: []string{"default"}, Concurrency: 1, At: time.Now()}
	if err := backend.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	denied := false
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Presence: backend, Authorize: func(_ context.Context, operation, scope, id string) error {
		if denied || operation != "inspect" {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	project := core.Project{Root: t.TempDir(), Commands: commands.Commands(commands.Factories{Client: func(context.Context, *core.Invocation) (*async.Client, error) { called++; return client, nil }})}
	var output bytes.Buffer
	options := core.Options{Stdout: &output, Stderr: &output}
	if err := core.Call(ctx, project, []string{"tasks", "inspect", "workers", "worker", "missing"}, options); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"status":"online"`)) || !bytes.Contains(output.Bytes(), []byte(`"status":"unknown"`)) || bytes.Contains(output.Bytes(), []byte(token)) {
		t.Fatal(output.String())
	}
	output.Reset()
	denied = true
	if err := core.Call(ctx, project, []string{"tasks", "inspect", "workers", "worker"}, options); !errors.Is(err, async.ErrDenied) || output.Len() != 0 {
		t.Fatal(output.String(), err)
	}
	before := called
	for _, args := range [][]string{{"tasks", "inspect"}, {"tasks", "inspect", "workers"}, {"tasks", "inspect", "workers", "a", "a"}, {"tasks", "inspect", "workers", "*"}, {"tasks", "inspect", "other", "a"}} {
		if err := core.Call(ctx, project, args, options); err == nil || called != before {
			t.Fatal(args, err, called)
		}
	}
}
