package views

import (
	"context"
	"errors"
)

// Rendering callbacks receive identifiers, CSRF state and sometimes an explicit
// reset-token form wrapper. Their panic values must not reach net/http's panic
// logger. No partial rendered bytes survive errors or cancellation.
func renderAccountPage[P any](ctx context.Context, render func(context.Context, P) ([]byte, error), page P) (body []byte, err error) {
	defer func() {
		if recover() != nil {
			body, err = nil, errors.New("auth views: account page rendering unavailable")
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err = render(ctx, page)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("auth views: account page rendering unavailable")
	}
	return body, nil
}
