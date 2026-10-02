package reports

import (
	"context"
	"github.com/Newton-School/gogo/async"
	"time"
)

const Queue = "reports"

// Both producers and workers import the same versioned contract. This pure
// handler is safe to execute again if delivery or acknowledgement is retried.
func Tasks() (*async.Registry, *async.Task[int64, int64], error) {
	registry := async.NewRegistry()
	task, err := async.Register(registry, "reports.double_count", 1,
		func(ctx context.Context, _ async.TaskContext, count int64) (int64, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if count < 0 || count > 1000000000 {
				return 0, async.ErrInvalid
			}
			return count * 2, nil
		}, async.TaskOptions{Queue: Queue, SoftLimit: 5 * time.Second, Authorize: func(ctx context.Context, task async.TaskContext) error {
			if task.Scope != "" {
				return async.ErrDenied
			}
			return ctx.Err()
		}})
	return registry, task, err
}
