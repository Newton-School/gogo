package queue_test

import (
	"context"
	"testing"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestRouterStaticDynamicDefaultAndExplicitRoutes(t *testing.T) {
	router := q.NewRouter(q.RouterOptions{
		DefaultQueue: "default",
		StaticRoutes: map[string]q.Route{
			"emails.send": {Queue: "emails", RoutingKey: "mail.send", Priority: 7},
		},
		DynamicRoutes: []q.RouteFunc{
			func(signature q.Signature) (q.Route, bool) {
				if signature.Headers["tenant"] == "vip" {
					return q.Route{Queue: "vip", RoutingKey: "vip.mail", Priority: 9}, true
				}
				return q.Route{}, false
			},
		},
	})

	static := router.Route(q.NewSignature("emails.send"), q.Task{Name: "emails.send"})
	if static.Queue != "emails" || static.RoutingKey != "mail.send" || static.Priority != 7 {
		t.Fatalf("static route = %#v", static)
	}
	dynamic := router.Route(q.NewSignature("emails.send").WithHeader("tenant", "vip"), q.Task{Name: "emails.send"})
	if dynamic.Queue != "vip" || dynamic.RoutingKey != "vip.mail" || dynamic.Priority != 9 {
		t.Fatalf("dynamic route = %#v", dynamic)
	}
	taskDefault := router.Route(q.NewSignature("reports.build"), q.Task{Name: "reports.build", Options: q.TaskOptions{Queue: "reports", RoutingKey: "reports.build", Priority: 3}})
	if taskDefault.Queue != "reports" || taskDefault.RoutingKey != "reports.build" || taskDefault.Priority != 3 {
		t.Fatalf("task default route = %#v", taskDefault)
	}
	explicit := router.Route(q.NewSignature("reports.build").WithQueue("critical").WithPriority(10), q.Task{Name: "reports.build", Options: q.TaskOptions{Queue: "reports", Priority: 3}})
	if explicit.Queue != "critical" || explicit.Priority != 10 {
		t.Fatalf("explicit route = %#v", explicit)
	}
}

func TestAppSendTaskAppliesRoutePriorityAndRoutingKey(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, _ = app.RegisterTask("jobs.low", func(context.Context, ...any) (any, error) { return nil, nil }, q.TaskOptions{})
	_, _ = app.RegisterTask("jobs.high", func(context.Context, ...any) (any, error) { return nil, nil }, q.TaskOptions{})
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	router := q.NewRouter(q.RouterOptions{
		DefaultQueue: "default",
		StaticRoutes: map[string]q.Route{
			"jobs.low":  {Queue: "work", RoutingKey: "work.low", Priority: 1},
			"jobs.high": {Queue: "work", RoutingKey: "work.high", Priority: 10},
		},
	})
	if _, err := app.SendTask(ctx, broker, q.NewSignature("jobs.low"), q.SendOptions{Router: router, ID: "low"}); err != nil {
		t.Fatalf("SendTask(low) error = %v", err)
	}
	if _, err := app.SendTask(ctx, broker, q.NewSignature("jobs.high"), q.SendOptions{Router: router, ID: "high"}); err != nil {
		t.Fatalf("SendTask(high) error = %v", err)
	}
	message, err := broker.Consume(ctx, "work", brokers.ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if message.Envelope.ID != "high" || message.Priority != 10 || message.Envelope.Queue != "work" || message.Envelope.Headers["routing_key"] != "work.high" {
		t.Fatalf("message = %#v", message)
	}
}

func TestWorkerQueueFilterOnlyConsumesSubscribedQueues(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, _ = app.RegisterTask("jobs.echo", func(_ context.Context, args ...any) (any, error) {
		return args[0], nil
	}, q.TaskOptions{AckPolicy: q.AckLate})
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "alpha", q.Envelope{ID: "alpha-task", Name: "jobs.echo", Args: []any{"alpha"}}, brokers.PublishOptions{})
	_, _ = broker.Publish(ctx, "beta", q.Envelope{ID: "beta-task", Name: "jobs.echo", Args: []any{"beta"}}, brokers.PublishOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"beta"}})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	beta, err := backend.GetResult(ctx, "beta-task")
	if err != nil || beta.Result != "beta" {
		t.Fatalf("beta result = %#v, %v", beta, err)
	}
	if _, err := backend.GetResult(ctx, "alpha-task"); err == nil {
		t.Fatal("alpha task should not be consumed by beta-only worker")
	}
}
