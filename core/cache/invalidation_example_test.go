package cache_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/cache"
)

func ExampleInvalidation_Apply() {
	// This example uses an explicit test-only provider. Applications supply
	// their configured cache backend and perform their own authorization.
	store := &invalidationStore{delete: func(context.Context, string) error { return nil }}
	plan, err := cache.NewInvalidation(cache.InvalidationConfig{
		Store: store, Namespace: "catalog",
		Keys: []string{cache.Key("catalog", 1, "tenant-a", "products")},
	})
	if err != nil {
		return
	}
	report, err := plan.Apply(context.Background())
	if err != nil {
		// Inspect the partial report; a failed attempt may already have deleted
		// its key. Do not replay the business mutation to retry invalidation.
		return
	}
	fmt.Println(report.Total, report.Attempted, report.Acknowledged)
	// Output: 1 1 1
}
