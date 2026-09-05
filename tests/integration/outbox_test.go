package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
)

type outboxEventSink func(context.Context, async.Event) error

func (f outboxEventSink) PublishEvent(ctx context.Context, event async.Event) error {
	return f(ctx, event)
}

func TestPostgresOutboxRollbackCommitRedis(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: []migrations.Migration{async.OutboxMigration()}}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Exec(ctx, "CREATE TABLE outbox_business (id integer PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	cfg := fixture.Start(t)
	cfg.Role = connector.TaskRole
	bc, err := connector.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	cfg.Role = connector.ResultRole
	rc, err := connector.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	broker := &adapter.Broker{Connection: bc}
	results := &adapter.Results{Connection: rc}
	registry := async.NewRegistry()
	calls := 0
	task, err := async.Register(registry, "test.outbox", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { calls++; return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	eventCalls := 0
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Events: outboxEventSink(func(_ context.Context, event async.Event) error {
		var state string
		if err := db.QueryRow(context.Background(), backend, "SELECT state FROM gogo_outbox WHERE task_id=$1", []any{event.TaskID}, &state); err != nil || state != "published" {
			t.Error("observer preceded durable outbox acknowledgement", state, err)
		}
		eventCalls++
		return errors.New("private event backend")
	})})
	if err != nil {
		t.Fatal(err)
	}
	outbox := &async.Outbox{Client: client, Backend: backend}
	signature, _ := task.Signature(41)
	if _, err := outbox.EnqueueOnCommit(ctx, signature); err == nil {
		t.Fatal("accepted outside transaction")
	}
	abort := errors.New("rollback")
	err = db.Atomic(ctx, backend, db.AtomicOptions{}, func(tx context.Context) error {
		if _, err := db.ExecutorFor(tx, backend).Exec(tx, "INSERT INTO outbox_business VALUES (1)"); err != nil {
			return err
		}
		if _, err := outbox.EnqueueOnCommit(tx, signature); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM gogo_outbox", nil, &count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM outbox_business", nil, &count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if eventCalls != 0 {
		t.Fatal("rollback produced a queue event", eventCalls)
	}
	var id string
	err = db.Atomic(ctx, backend, db.AtomicOptions{}, func(tx context.Context) error {
		if _, err := db.ExecutorFor(tx, backend).Exec(tx, "INSERT INTO outbox_business VALUES (1)"); err != nil {
			return err
		}
		var err error
		id, err = outbox.EnqueueOnCommit(tx, signature)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if eventCalls != 1 {
		t.Fatal("confirmed dispatch observation missing", eventCalls)
	}
	delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker", Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "worker"}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	result := async.RestoreResult[int](client, id)
	value, err := result.Get(ctx)
	if err != nil || value != 42 || calls != 1 {
		t.Fatal(value, calls, err)
	}
	var state string
	if err := db.QueryRow(ctx, backend, "SELECT state FROM gogo_outbox WHERE task_id=$1", []any{id}, &state); err != nil || state != "published" {
		t.Fatal(state, err)
	}
}
