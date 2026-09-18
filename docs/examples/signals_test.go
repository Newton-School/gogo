package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/signals"
)

func Example_signal() {
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
	results, err := published.Send(context.Background(), Published{ProductID: 42})
	if err != nil {
		panic(err)
	}
	fmt.Println("receivers called:", len(results))
	// Output:
	// published product: 42
	// receivers called: 1
}
