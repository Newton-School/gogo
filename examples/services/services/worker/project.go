package main

import (
	"context"
	"example.com/gogo-services/apps/reports"
	"example.com/gogo-services/config/settings"
	"example.com/gogo-services/internal/taskqueue"
	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/async"
	asyncmanagement "github.com/Newton-School/gogo/async/management"
	"github.com/Newton-School/gogo/core/management"
)

func Project() gogo.Project {
	queue := &taskqueue.Connection{}
	return gogo.Project{Name: "reports-worker", MainPackage: "./services/worker", Schema: settings.Schema(), ResourceFactory: queue.Resources,
		Commands: asyncmanagement.Commands(asyncmanagement.Factories{WorkerResources: []string{"redis"}, Worker: func(context.Context, *management.Invocation) (asyncmanagement.WorkerRuntime, error) {
			runtime, err := queue.Runtime()
			if err != nil {
				return asyncmanagement.WorkerRuntime{}, err
			}
			id, err := async.NewID()
			if err != nil {
				return asyncmanagement.WorkerRuntime{}, err
			}
			return asyncmanagement.WorkerRuntime{
				Worker:  &async.Worker{ID: "reports-" + id, Registry: runtime.Registry, Client: runtime.Client, Broker: runtime.Broker, Results: runtime.Results, Queues: []string{reports.Queue}, Concurrency: 2},
				Relay:   &async.IntentRelay{Client: runtime.Client, Sources: []async.IntentStore{runtime.Results, runtime.Workflows}, ID: "reports-relay-" + id},
				Delayed: &async.DelayedDispatcher{Client: runtime.Client, ID: "reports-delayed-" + id},
			}, nil
		}, ClientResources: []string{"redis"}, Client: func(context.Context, *management.Invocation) (*async.Client, error) {
			runtime, err := queue.Runtime()
			if err != nil {
				return nil, err
			}
			return runtime.Client, nil
		}}),
	}
}
