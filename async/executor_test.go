package async_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

func childRegistry(t *testing.T) (*async.Registry, *async.Task[int, int]) {
	t.Helper()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.child", 1, func(ctx context.Context, tc async.TaskContext, n int) (int, error) {
		if n < 0 {
			select {}
		}
		return n + 1, nil
	}, async.TaskOptions{HardLimit: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return registry, task
}
func TestTaskChildHelper(t *testing.T) {
	if os.Getenv("GOGO_TEST_TASK_CHILD") != "1" {
		return
	}
	registry, _ := childRegistry(t)
	if err := registry.ServeChild(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func TestProcessExecutorResultAndHardDeadline(t *testing.T) {
	registry, task := childRegistry(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executor := &async.ProcessExecutor{Command: []string{binary, "-test.run=^TestTaskChildHelper$"}, Environment: append(os.Environ(), "GOGO_TEST_TASK_CHILD=1")}
	_ = registry
	signature, _ := task.Signature(41)
	id, _ := async.NewID()
	max := 3
	e := async.Envelope{ProtocolVersion: 1, ID: id, Task: signature.Task, Version: 1, Args: signature.Args, CreatedAt: time.Now(), Queue: "default", MaxRetries: &max}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := executor.Execute(ctx, async.Execution{Envelope: e, TaskContext: async.TaskContext{ID: id}})
	if err != nil || string(out) != "42" {
		t.Fatal(string(out), err)
	}
	bad, _ := task.Signature(-1)
	e.Args = bad.Args
	deadline, cancelDeadline := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelDeadline()
	started := time.Now()
	_, err = executor.Execute(deadline, async.Execution{Envelope: e, TaskContext: async.TaskContext{ID: id}})
	if err == nil || time.Since(started) > time.Second {
		t.Fatal(err, time.Since(started))
	}
}

func TestMapChunksAndStarMap(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.square", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n * n, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := async.RegisterMap(registry, "test.squares", 1, task, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := mapped.Apply(ctx, []int{1, 2, 3})
	if err != nil || len(out) != 3 || out[2] != 9 {
		t.Fatal(out, err)
	}
	chunks, err := async.Chunks(mapped, []int{1, 2, 3, 4, 5}, 2)
	if err != nil || len(chunks.Signatures) != 3 {
		t.Fatal(chunks, err)
	}
	if _, err := async.Chunks(mapped, []int{1}, 0); err == nil {
		t.Fatal("invalid chunk size accepted")
	}
}
