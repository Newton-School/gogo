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
	schedules *asyncredis.Schedules
	workers   *asyncredis.Workers
	events    *asyncredis.Events
	beats     *asyncredis.Beats
}

func (c *Connections) taskRuntime() (*taskRuntime, error) {
	tasks, err := catalog.NewTasks()
	if err != nil {
		return nil, err
	}
	broker := &asyncredis.Broker{Connection: c.Tasks, Queues: []string{"showcase"}}
	results := &asyncredis.Results{Connection: c.Results}
	workflows := &asyncredis.Workflows{Connection: c.Results}
	schedules := &asyncredis.Schedules{Connection: c.Results}
	workers := &asyncredis.Workers{Connection: c.Results}
	events := &asyncredis.Events{Connection: c.Results}
	beats := &asyncredis.Beats{Connection: c.Results}
	client, err := async.NewClient(async.ClientConfig{Registry: tasks.Registry, Broker: broker, Results: results, Workflows: workflows, Schedules: schedules, Presence: workers, Events: events, Queues: []string{"showcase"}})
	if err != nil {
		return nil, err
	}
	return &taskRuntime{tasks, client, broker, results, workflows, schedules, workers, events, beats}, nil
}

func (c *Connections) AsyncCommands() []management.Command {
	commands := asyncmanagement.Commands(asyncmanagement.Factories{
		WorkerResources: []string{"redis"}, ClientResources: []string{"redis"}, BeatResources: []string{"redis"},
		Beat: func(context.Context, *management.Invocation) (asyncmanagement.BeatRuntime, error) {
			r, err := c.taskRuntime()
			if err != nil {
				return asyncmanagement.BeatRuntime{}, err
			}
			id, err := async.NewID()
			if err != nil {
				return asyncmanagement.BeatRuntime{}, err
			}
			return asyncmanagement.BeatRuntime{Beat: &async.Beat{Client: r.client, Store: r.schedules, ID: "beat-" + id, Monitor: r.beats}, Relay: &async.IntentRelay{Client: r.client, Sources: []async.IntentStore{r.schedules}, ID: "beat-relay-" + id}}, nil
		},
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
				Worker:  &async.Worker{Registry: r.tasks.Registry, Client: r.client, Broker: r.broker, Results: r.results, Queues: []string{"showcase"}, ID: "showcase-" + id, Concurrency: 2, Presence: r.workers, Events: r.events},
				Relay:   &async.IntentRelay{Client: r.client, Sources: []async.IntentStore{r.results, r.workflows}, ID: "relay-" + id},
				Delayed: &async.DelayedDispatcher{Client: r.client, ID: "delayed-" + id},
			}, nil
		},
	})
	return append(commands, management.Command{Name: "demoasync", Help: "Run task/workflow examples or install a periodic schedule (run worker and beat separately)", Resources: []string{"redis"}, OpenResources: true,
		Validate: func(args []string) error {
			if len(args) != 1 {
				return errors.New("choose task, delayed, group, chain, chord or schedule")
			}
			switch args[0] {
			case "task", "delayed", "group", "chain", "chord", "schedule":
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
				if args[0] == "schedule" {
					sig, err := r.tasks.Double.Signature(21)
					if err != nil {
						return err
					}
					schedule := async.PeriodicSchedule{ID: async.StableID("showcase", "double-minute"), Signature: sig, Rule: async.Every(time.Minute), Enabled: true, NextDue: time.Now().UTC().Add(time.Minute), Misfire: "coalesce", CatchUpLimit: 1, Overlap: "skip", Revision: 1}
					if err := r.schedules.UpsertSchedule(ctx, schedule, 0); err != nil {
						return err
					}
					return json.NewEncoder(i.Stdout).Encode(map[string]any{"schedule_id": schedule.ID, "installed": true})
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
