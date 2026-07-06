package testing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	stdtesting "testing"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/email"
	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/queue"
)

func TestMailOutboxCapturesAndClearsMessages(t *stdtesting.T) {
	outbox := NewMailOutbox()
	sent, err := outbox.Send(context.Background(), email.Message{Subject: "Welcome", To: []string{"user@example.com"}})
	if err != nil || sent != 1 {
		t.Fatalf("Send() = %d, %v", sent, err)
	}
	outbox.AssertEmailSent(t, "Welcome")
	if len(outbox.Messages()) != 1 {
		t.Fatalf("Messages() = %#v", outbox.Messages())
	}
	outbox.Clear()
	if len(outbox.Messages()) != 0 {
		t.Fatalf("Messages() after Clear = %#v", outbox.Messages())
	}
}

func TestEagerQueueRunsTasksStoresResultsAndExposesFakeBrokerBackend(t *stdtesting.T) {
	ctx := context.Background()
	app := queue.NewApp(queue.AppOptions{})
	_, _ = app.RegisterTask("math.add", func(_ context.Context, args ...any) (any, error) {
		return args[0].(int) + args[1].(int), nil
	}, queue.TaskOptions{})
	failure := errors.New("boom")
	_, _ = app.RegisterTask("jobs.fail", func(context.Context, ...any) (any, error) {
		return nil, failure
	}, queue.TaskOptions{})

	harness := NewQueueHarness(app)
	success, err := harness.Apply(ctx, queue.NewSignature("math.add", 1, 2))
	if err != nil || success.State != queue.StateSuccess || success.Result != 3 {
		t.Fatalf("Apply(success) = %#v, %v", success, err)
	}
	stored, err := harness.Backend.GetResult(ctx, success.TaskID)
	if err != nil || stored.Result != 3 {
		t.Fatalf("stored result = %#v, %v", stored, err)
	}

	failed, err := harness.Apply(ctx, queue.NewSignature("jobs.fail"))
	if !errors.Is(err, failure) || failed.State != queue.StateFailure || failed.Error != "boom" {
		t.Fatalf("Apply(failure) = %#v, %v", failed, err)
	}

	if _, err := harness.Enqueue(ctx, queue.NewSignature("math.add", 3, 4).WithQueue("critical")); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	harness.AssertTaskEnqueued(t, "math.add")
	queues, err := harness.Broker.InspectQueues(ctx)
	if err != nil || len(queues) != 1 || queues[0].Name != "critical" || queues[0].Ready != 1 {
		t.Fatalf("InspectQueues() = %#v, %v", queues, err)
	}
}

func TestAdminLoginAndAdminPageAssertions(t *stdtesting.T) {
	registry := admin.NewRegistry()
	if err := registry.RegisterMetadata(models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}, admin.ModelAdmin{ListDisplay: []string{"title"}}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !user.IsStaff || !user.IsAuthenticated() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = fmt.Fprint(w, "Admin page blog.Post title")
	})

	client := NewAdminClient(handler).Login(auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true},
		Username:         "admin",
		IsStaff:          true,
	}})
	response := client.Get("/admin/")
	response.AssertStatus(t, http.StatusOK)
	AssertAdminModelRegistered(t, registry, "blog.Post")
	AssertAdminPage(t, response, "blog.Post")
	AssertAdminColumn(t, response, "title")
}
