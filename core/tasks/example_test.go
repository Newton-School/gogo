package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Newton-School/gogo/core/tasks"
)

// This fixture is a test-only provider with a prearranged result. It neither
// executes a task nor represents a production in-process/eager backend.
type exampleDispatcher struct{}
type exampleResult struct{}

func (exampleDispatcher) Enqueue(context.Context, tasks.Request) (tasks.Result, error) {
	return exampleResult{}, nil
}
func (exampleResult) ID() string { return "00000000-0000-4000-8000-000000000001" }
func (exampleResult) Get(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"count":42}`), nil
}

func ExampleDispatcher() {
	// Application code depends only on Core. Production composition supplies an
	// explicitly configured implementation, such as async.NewCoreDispatcher.
	var dispatcher tasks.Dispatcher = exampleDispatcher{}
	ctx := context.Background()
	result, err := dispatcher.Enqueue(ctx, tasks.Request{
		Task: "reports.count", Version: 1, Args: json.RawMessage(`{"report":"summary"}`),
	})
	if err != nil {
		// An AcceptanceError may include a nonnil result: retain its identity and
		// do not blindly create a new task. This example's fixture cannot fail.
		return
	}
	output, err := result.Get(ctx)
	if err != nil {
		return
	}
	fmt.Println(result.ID())
	fmt.Println(string(output))
	// Output:
	// 00000000-0000-4000-8000-000000000001
	// {"count":42}
}
