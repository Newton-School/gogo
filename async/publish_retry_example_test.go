package async_test

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Newton-School/gogo/async"
)

func ExamplePublishRetryPolicy() {
	// Bootstrap supplies actual configured providers and explicit grants. This
	// constructor example opens no service and dispatches no work on its own.
	build := func(registry *async.Registry, broker async.Broker, results async.ResultStore,
		authorize func(context.Context, string, string, string) error) (*async.Client, error) {
		return async.NewClient(async.ClientConfig{
			Registry: registry, Broker: broker, Results: results, Authorize: authorize,
			PublishRetry: &async.PublishRetryPolicy{
				MaxAttempts: 3, InitialDelay: 100 * time.Millisecond,
				MaxDelay: time.Second, MaxElapsed: 5 * time.Second,
				Retryable: func(err error) bool {
					// A lost reply may follow an applied write. This application
					// permits replay and keeps task side effects idempotent.
					return err == async.ErrBusy || err == async.ErrUnavailable || errors.Is(err, io.ErrUnexpectedEOF)
				},
			},
		})
	}
	_ = build
}
