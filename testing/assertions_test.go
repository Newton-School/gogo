package testing

import (
	"context"
	"errors"
	stdtesting "testing"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/email"
	"github.com/Newton-School/gogo/forms"
	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/signals"
)

func TestFrameworkAssertionsCoverJSONFormsSignalsMailQueueAndPermissions(t *stdtesting.T) {
	AssertEqualJSON(t, `{"b":2,"a":{"name":"gogo"}}`, map[string]any{"a": map[string]any{"name": "gogo"}, "b": 2})

	fieldErrors := forms.ErrorDict{}
	fieldErrors.Add("title", forms.ValidationError{Message: "required"})
	AssertHasFieldError(t, fieldErrors, "title", "required")
	AssertHasNonFieldError(t, forms.NonFieldErrorList{{Message: "invalid form"}}, "invalid form")

	counter := NewQueryCounter()
	counter.Record()
	counter.Record()
	AssertQueryCount(t, counter, 2)

	signal := signals.New[string]("saved")
	signal.Connect(nil, func(context.Context, any, string) error { return nil })
	AssertSignalSent(t, signal.Send(context.Background(), nil, "post"))

	outbox := NewMailOutbox()
	_, _ = outbox.Send(context.Background(), email.Message{Subject: "Welcome"})
	AssertEmailSent(t, outbox, "Welcome")

	app := queue.NewApp(queue.AppOptions{})
	_, _ = app.RegisterTask("blog.publish", func(context.Context, ...any) (any, error) { return nil, nil }, queue.TaskOptions{})
	harness := NewQueueHarness(app)
	_, _ = harness.Enqueue(context.Background(), queue.NewSignature("blog.publish"))
	AssertTaskEnqueued(t, harness, "blog.publish")

	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{
		IsActive:        true,
		UserPermissions: []auth.Permission{{Codename: "view_post", ContentType: auth.ContentType{AppLabel: "blog", Model: "post"}}},
	}}}
	AssertPermissionGranted(t, user, "blog.view_post")
	AssertPermissionDenied(t, user, "blog.change_post")
}

func TestAssertSignalSentFailsWhenReceiverErrors(t *stdtesting.T) {
	errBoom := errors.New("boom")
	signal := signals.New[string]("saved")
	signal.Connect(nil, func(context.Context, any, string) error { return errBoom })
	assertion := captureFatal(func(t TestHelper) {
		AssertSignalSent(t, signal.Send(context.Background(), nil, "post"))
	})
	if assertion == "" {
		t.Fatal("AssertSignalSent should fail when receiver errors")
	}
}

func captureFatal(fn func(TestHelper)) string {
	helper := &fatalRecorder{}
	fn(helper)
	return helper.message
}

type fatalRecorder struct {
	message string
}

func (f *fatalRecorder) Helper() {}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.message = format
}
