package async_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func ExampleGroupResult_Forget() {
	// The explicit test backend keeps this example executable without services.
	// Production config supplies authorized, durable result/workflow providers.
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{
		Registry: async.NewRegistry(), Broker: backend, Results: backend,
		Workflows: backend,
		Authorize: func(context.Context, string, string, string) error {
			return nil // Deliberately public, unscoped example only.
		},
	})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	group, err := client.ApplyCanvas(ctx, async.Group())
	if err != nil {
		panic(err)
	}
	if err := group.Forget(ctx); err != nil {
		panic(err)
	}
	status, err := group.Snapshot(ctx)
	if err != nil {
		panic(err)
	}
	_, err = group.Join(ctx)
	fmt.Println(status.State, status.PayloadForgotten, errors.Is(err, async.ErrResultExpired))
	// Output: SUCCEEDED true true
}
