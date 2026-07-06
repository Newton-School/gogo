package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestQueueWorkerCommandParsesFlagsAndRunsOnce(t *testing.T) {
	ctx := context.Background()
	runtime := NewQueueRuntime()
	_, _ = runtime.App.RegisterTask("jobs.ok", func(context.Context, ...any) (any, error) { return "ok", nil }, q.TaskOptions{AckPolicy: q.AckLate})
	_, _ = runtime.Broker.Publish(ctx, "default", q.Envelope{ID: "task-1", Name: "jobs.ok"}, brokers.PublishOptions{})
	var stdout bytes.Buffer
	command := NewWorkerCommand(runtime)
	err := command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(ctx, []string{
		"--once",
		"--concurrency", "2",
		"--autoscale", "1,4",
		"--queues", "default",
		"--hostname", "worker-cli",
		"--log-level", "debug",
		"--broker-url", "memory://",
		"--result-backend", "memory",
		"--pool", "solo",
		"--prefetch-multiplier", "3",
		"--max-tasks-per-child", "10",
		"--max-memory-per-child", "1073741824",
		"--soft-time-limit", "1s",
		"--hard-time-limit", "2s",
		"--graceful-timeout", "3s",
		"--accepted-serializers", "application/json,application/octet-stream",
	}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("worker command error = %v", err)
	}
	if !strings.Contains(stdout.String(), "worker worker-cli processed one task") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	result, err := runtime.Backend.GetResult(ctx, "task-1")
	if err != nil || result.State != q.StateSuccess {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestQueueWorkerCheckRejectsUnreachableRedisBrokerURL(t *testing.T) {
	var stdout bytes.Buffer
	err := NewWorkerCommand(NewQueueRuntime()).(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{
		"--check",
		"--broker-url", "redis://127.0.0.1:1/0",
		"--result-backend", "memory",
	}, &stdout, io.Discard)
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("worker --check error = %v, want ErrCommandFailed", err)
	}
	if !strings.Contains(err.Error(), "Redis broker is not reachable") {
		t.Fatalf("worker --check error = %v, want Redis reachability context", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("worker --check stdout = %q, want empty on invalid runtime", stdout.String())
	}
}

func TestQueueBeatInspectAndQueuesCommands(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	runtime := NewQueueRuntime()
	runtime.Now = func() time.Time { return now }
	_, _ = runtime.App.RegisterTask("jobs.scheduled", func(context.Context, ...any) (any, error) { return nil, nil }, q.TaskOptions{})
	_ = runtime.Store.Save(ctx, q.ScheduleEntry{
		Name:      "scheduled",
		Signature: q.NewSignature("jobs.scheduled"),
		Schedule:  q.ClockedSchedule{RunAt: now},
		Enabled:   true,
		Send:      q.SendOptions{ID: "scheduled-task"},
	})
	var beatOut bytes.Buffer
	if err := NewBeatCommand(runtime).(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(ctx, []string{"--once", "--schedule-path", "memory://", "--broker-url", "memory://"}, &beatOut, io.Discard); err != nil {
		t.Fatalf("beat command error = %v", err)
	}
	if !strings.Contains(beatOut.String(), "beat enqueued 1 task") {
		t.Fatalf("beat stdout = %q", beatOut.String())
	}

	var inspectOut bytes.Buffer
	if err := NewInspectCommand(runtime).(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(ctx, []string{"--report", "--ping"}, &inspectOut, io.Discard); err != nil {
		t.Fatalf("inspect command error = %v", err)
	}
	if !strings.Contains(inspectOut.String(), "registered=1") || !strings.Contains(inspectOut.String(), "pong") {
		t.Fatalf("inspect stdout = %q", inspectOut.String())
	}

	var queuesOut bytes.Buffer
	if err := NewQueuesCommand(runtime).(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(ctx, []string{"--queue", "default"}, &queuesOut, io.Discard); err != nil {
		t.Fatalf("queues command error = %v", err)
	}
	if !strings.Contains(queuesOut.String(), "default ready=1") {
		t.Fatalf("queues stdout = %q", queuesOut.String())
	}
}

func TestQueuesCommandReportsEmptyState(t *testing.T) {
	var stdout bytes.Buffer
	if err := NewQueuesCommand(NewQueueRuntime()).(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), nil, &stdout, io.Discard); err != nil {
		t.Fatalf("queues command error = %v", err)
	}
	if stdout.String() != "no queues found\n" {
		t.Fatalf("queues stdout = %q", stdout.String())
	}
}
