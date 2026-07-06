package blog

import (
	"context"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/email"
	"github.com/Newton-School/gogo/queue"
)

func RegisterTasks(app *queue.App, backend email.Backend) error {
	if app == nil {
		return fmt.Errorf("blog queue app is required")
	}
	if backend == nil {
		return fmt.Errorf("blog email backend is required")
	}
	if _, err := app.RegisterTask("blog.send_comment_notification", sendCommentNotification(backend), queue.TaskOptions{
		Queue:             "mail",
		MaxRetries:        3,
		DefaultRetryDelay: 30 * time.Second,
		RetryBackoff:      true,
		RetryJitter:       true,
		SoftTimeout:       5 * time.Second,
		HardTimeout:       10 * time.Second,
	}); err != nil {
		return err
	}
	if _, err := app.RegisterTask("blog.audit_event", auditEventTask, queue.TaskOptions{
		Queue:        "audit",
		MaxRetries:   1,
		TrackStarted: true,
		AckPolicy:    queue.AckLate,
	}); err != nil {
		return err
	}
	return nil
}

func sendCommentNotification(backend email.Backend) queue.TaskFunc {
	return func(ctx context.Context, args ...any) (any, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("recipient and post title are required")
		}
		recipient := fmt.Sprint(args[0])
		if recipient == "" {
			return nil, fmt.Errorf("recipient is required")
		}
		postTitle := fmt.Sprint(args[1])
		message := email.Message{
			Subject: "New blog comment",
			Body:    fmt.Sprintf("A new comment was submitted for %q and is waiting for moderation.", postTitle),
			From:    "no-reply@gogo.local",
			To:      []string{recipient},
			Headers: map[string]string{"X-Gogo-Task": "blog.send_comment_notification"},
		}
		sent, err := backend.SendMessages(ctx, []email.Message{message})
		if err != nil {
			return nil, err
		}
		return sent, nil
	}
}

func auditEventTask(ctx context.Context, args ...any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("action and object reference are required")
	}
	action := fmt.Sprint(args[0])
	objectRef := fmt.Sprint(args[1])
	if action == "" || objectRef == "" {
		return nil, fmt.Errorf("action and object reference are required")
	}
	return fmt.Sprintf("audit:%s:%s", action, objectRef), nil
}
