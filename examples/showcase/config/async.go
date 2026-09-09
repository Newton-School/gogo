package config

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"time"

	"example.com/gogo-showcase/apps/catalog"
	"github.com/Newton-School/gogo/async"
	asyncmanagement "github.com/Newton-School/gogo/async/management"
	asyncredis "github.com/Newton-School/gogo/async/redis"
	"github.com/Newton-School/gogo/core/management"
)

type taskRuntime struct {
	tasks     *catalog.Tasks
	client    *async.Client
	broker    *asyncredis.Broker
	results   *asyncredis.Results
	workflows *asyncredis.Workflows
}

func (c *Connections) taskRuntime() (*taskRuntime, error) {
	tasks, err := catalog.NewTasks()
	if err != nil {
		return nil, err
	}
	broker := &asyncredis.Broker{Connection: c.Tasks, Queues: []string{"showcase"}}
	results := &asyncredis.Results{Connection: c.Results}
	workflows := &asyncredis.Workflows{Connection: c.Results}
	client, err := async.NewClient(async.ClientConfig{Registry: tasks.Registry, Broker: broker, Results: results, Workflows: workflows, Schedules: &asyncredis.Schedules{Connection: c.Results}, Queues: []string{"showcase"}})
	if err != nil {
		return nil, err
	}
	return &taskRuntime{tasks, client, broker, results, workflows}, nil
}

func (c *Connections) AsyncCommands() []management.Command {
	commands := asyncmanagement.Commands(asyncmanagement.Factories{
		WorkerResources: []string{"redis"}, ClientResources: []string{"redis"},
		Client: func(context.Context, *management.Invocation) (*async.Client, error) {
			runtime, err := c.taskRuntime()
			if err != nil {
				return nil, err
			}
			return runtime.client, nil
		},
		Worker: func(context.Context, *management.Invocation) (asyncmanagement.WorkerRuntime, error) {
			r, err := c.taskRuntime()
			if err != nil {
				return asyncmanagement.WorkerRuntime{}, err
			}
			id, err := async.NewID()
			if err != nil {
				return asyncmanagement.WorkerRuntime{}, err
			}
			return asyncmanagement.WorkerRuntime{
				Worker:  &async.Worker{Registry: r.tasks.Registry, Client: r.client, Broker: r.broker, Results: r.results, Queues: []string{"showcase"}, ID: "showcase-" + id, Concurrency: 2},
				Relay:   &async.IntentRelay{Client: r.client, Sources: []async.IntentStore{r.results, r.workflows}, ID: "relay-" + id},
				Delayed: &async.DelayedDispatcher{Client: r.client, ID: "delayed-" + id},
			}, nil
		},
	})
	return append(commands, management.Command{Name: "demoasync", Help: "Queue and await task, delayed task, group, chain or chord (run worker separately)", Resources: []string{"redis"}, OpenResources: true,
		Validate: func(args []string) error {
			if len(args) != 1 {
				return errors.New("choose task, delayed, group, chain or chord")
			}
			switch args[0] {
			case "task", "delayed", "group", "chain", "chord":
				return nil
			}
			return errors.New("unknown demonstration")
		},
		Configure: func(*flag.FlagSet) management.Runner {
			return func(ctx context.Context, i *management.Invocation, args []string) error {
				ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
				defer cancel()
				r, err := c.taskRuntime()
				if err != nil {
					return err
				}
				if args[0] == "task" || args[0] == "delayed" {
					var options []async.DispatchOption
					if args[0] == "delayed" {
						options = append(options, async.WithCountdown(2*time.Second))
					}
					result, err := r.tasks.Double.Delay(ctx, r.client, 21, options...)
					if err != nil {
						return err
					}
					if err := json.NewEncoder(i.Stdout).Encode(map[string]any{"task_id": result.Receipt.ID, "accepted": true}); err != nil {
						return err
					}
					value, err := result.Get(ctx)
					if err != nil {
						return err
					}
					return json.NewEncoder(i.Stdout).Encode(map[string]any{"result": value})
				}
				canvas, err := r.tasks.Canvas(args[0])
				if err != nil {
					return err
				}
				result, err := r.client.ApplyCanvas(ctx, canvas)
				if err != nil {
					return err
				}
				if err := json.NewEncoder(i.Stdout).Encode(map[string]any{"workflow_id": result.ID, "accepted": true}); err != nil {
					return err
				}
				values, err := result.Join(ctx)
				if err != nil {
					return err
				}
				return json.NewEncoder(i.Stdout).Encode(map[string]any{"results": values})
			}
		},
	})
}
