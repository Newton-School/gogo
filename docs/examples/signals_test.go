package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/signals"
)

func Example_signal() {
	// docs:begin signal-connect
	type Published struct{ ProductID int64 }
	var published signals.Signal[Published]
	err := published.Connect("search-index", 10, func(ctx context.Context, event Published) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Println("published product:", event.ProductID)
		return nil
	})
	if err != nil {
		panic(err)
	}
	// docs:end signal-connect
	// docs:begin signal-send
	results, err := published.Send(context.Background(), Published{ProductID: 42})
	if err != nil {
		panic(err)
	}
	fmt.Println("receivers called:", len(results))
	// docs:end signal-send
	// Output:
	// published product: 42
	// receivers called: 1
}
