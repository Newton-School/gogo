// Package management connects optional Async roles to the project's central
// management entry point without making the core framework import Async.
package management

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Newton-School/gogo/async"
	core "github.com/Newton-School/gogo/core/management"
)

type WorkerRuntime struct {
	Worker  *async.Worker
	Relay   *async.IntentRelay
	Delayed *async.DelayedDispatcher
	Outbox  *async.Outbox
}

type BeatRuntime struct {
	Beat  *async.Beat
	Relay *async.IntentRelay
}

// Factories are called only after management parses arguments, validates
// resource-scoped configuration and starts the application's selected resources.
// A nil factory omits its command entirely. Resources name the caller's public
// configuration/resource roles; these are never inferred from URLs or backends.
type Factories struct {
	Worker func(context.Context, *core.Invocation) (WorkerRuntime, error)
	Beat   func(context.Context, *core.Invocation) (BeatRuntime, error)
	Client func(context.Context, *core.Invocation) (*async.Client, error)
	// Child explicitly installs the task-child IPC entry point used only by a
	// configured ProcessExecutor. It does not accept executable code or argv
	// from task messages, and starts its declared application resources first.
	Child           func(context.Context, *core.Invocation) (*async.Registry, error)
	WorkerResources []string
	BeatResources   []string
	ClientResources []string
	ChildResources  []string
}

var queueName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,191}$`)
var taskID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func noArgs(args []string) error {
	if len(args) != 0 {
		return async.ErrInvalid
	}
	return nil
}

func Commands(f Factories) []core.Command {
	var commands []core.Command
	if f.Worker != nil {
		commands = append(commands, core.Command{Name: "worker", Help: "Run leased Async workers and configured recovery roles", Resources: append([]string(nil), f.WorkerResources...), OpenResources: true, Validate: noArgs, Configure: func(flags *flag.FlagSet) core.Runner {
			once := flags.Bool("once", false, "Process at most one reservation and recovery pass")
			concurrency := 0
			var queues []string
			flags.Func("concurrency", "Bound simultaneous task handlers (1..1024)", func(text string) error {
				value, err := strconv.Atoi(text)
				if err != nil || value < 1 || value > 1024 {
					return async.ErrInvalid
				}
				concurrency = value
				return nil
			})
			flags.Func("queues", "Comma-separated declared queues", func(text string) error {
				values := strings.Split(text, ",")
				if len(values) > 64 {
					return async.ErrInvalid
				}
				for _, value := range values {
					if !queueName.MatchString(value) {
						return async.ErrInvalid
					}
				}
				queues = values
				return nil
			})
			return func(ctx context.Context, invocation *core.Invocation, _ []string) error {
				runtime, err := f.Worker(ctx, invocation)
				if err != nil {
					return err
				}
				if runtime.Worker == nil {
					return async.ErrInvalid
				}
				if concurrency != 0 {
					runtime.Worker.Concurrency = concurrency
				}
				if queues != nil {
					runtime.Worker.Queues = queues
				}
				var ticks []func(context.Context) error
				if runtime.Outbox != nil {
					ticks = append(ticks, runtime.Outbox.Tick)
				}
				if runtime.Relay != nil {
					ticks = append(ticks, runtime.Relay.Tick)
				}
				if runtime.Delayed != nil {
					ticks = append(ticks, runtime.Delayed.Tick)
				}
				if *once {
					for _, tick := range ticks {
						if err := tick(ctx); err != nil {
							return err
						}
					}
					if err := runtime.Worker.RunOnce(ctx); err != nil {
						return err
					}
					if runtime.Relay != nil {
						return runtime.Relay.Tick(ctx)
					}
					return nil
				}
				runners := []func(context.Context) error{runtime.Worker.Run}
				for _, tick := range ticks {
					runners = append(runners, poll(tick))
				}
				return supervise(ctx, runners, invocation.Settings.Duration("GOGO_SHUTDOWN_GRACE"))
			}
		}})
	}
	if f.Beat != nil {
		commands = append(commands, core.Command{Name: "beat", Help: "Run periodic schedule leases and configured dispatch relay", Resources: append([]string(nil), f.BeatResources...), OpenResources: true, Validate: noArgs, Configure: func(flags *flag.FlagSet) core.Runner {
			once := flags.Bool("once", false, "Commit at most one scheduler scan")
			return func(ctx context.Context, invocation *core.Invocation, _ []string) error {
				runtime, err := f.Beat(ctx, invocation)
				if err != nil {
					return err
				}
				if runtime.Beat == nil {
					return async.ErrInvalid
				}
				if *once {
					if err := runtime.Beat.Tick(ctx); err != nil {
						return err
					}
					if runtime.Relay != nil {
						return runtime.Relay.Tick(ctx)
					}
					return nil
				}
				runners := []func(context.Context) error{runtime.Beat.Run}
				if runtime.Relay != nil {
					runners = append(runners, runtime.Relay.Run)
				}
				return supervise(ctx, runners, invocation.Settings.Duration("GOGO_SHUTDOWN_GRACE"))
			}
		}})
	}
	if f.Client != nil {
		commands = append(commands, core.Command{Name: "tasks", Help: "Read scoped task status/result or request revoke", Resources: append([]string(nil), f.ClientResources...), OpenResources: true, Validate: func(args []string) error {
			if len(args) != 2 || !taskID.MatchString(args[1]) || args[0] != "status" && args[0] != "result" && args[0] != "revoke" {
				return async.ErrInvalid
			}
			return nil
		}, Configure: func(*flag.FlagSet) core.Runner {
			return func(ctx context.Context, invocation *core.Invocation, args []string) error {
				client, err := f.Client(ctx, invocation)
				if err != nil {
					return err
				}
				if client == nil {
					return async.ErrInvalid
				}
				result := async.RestoreResult[json.RawMessage](client, args[1])
				if args[0] == "revoke" {
					// This command returns current state as well as the mutation
					// receipt; require its read grant before making any change.
					if _, err := result.Snapshot(ctx); err != nil {
						return err
					}
					if err := result.Revoke(ctx); err != nil {
						return err
					}
					record, err := result.Snapshot(ctx)
					if err != nil {
						return err
					}
					return json.NewEncoder(invocation.Stdout).Encode(map[string]any{"task_id": args[1], "state": record.State, "cancellation_requested": record.CancelRequested})
				}
				if args[0] == "result" {
					value, err := result.Get(ctx)
					if err != nil {
						return err
					}
					return json.NewEncoder(invocation.Stdout).Encode(value)
				}
				record, err := result.Snapshot(ctx)
				if err != nil {
					return err
				}
				// Status never prints task arguments, headers, principal, progress
				// or result payload; result access is an explicit separate action.
				return json.NewEncoder(invocation.Stdout).Encode(map[string]any{"task_id": args[1], "state": record.State, "retries": record.Envelope.Retries, "delivery_count": record.DeliveryCount, "cancellation_requested": record.CancelRequested, "replacement_id": record.ReplacementID})
			}
		}}, core.Command{Name: "queues", Help: "Inspect declared queue counters through the scope policy", Resources: append([]string(nil), f.ClientResources...), OpenResources: true, Validate: func(args []string) error {
			if len(args) < 2 || len(args) > 65 || args[0] != "inspect" {
				return async.ErrInvalid
			}
			for _, queue := range args[1:] {
				if !queueName.MatchString(queue) {
					return async.ErrInvalid
				}
			}
			return nil
		}, Configure: func(*flag.FlagSet) core.Runner {
			return func(ctx context.Context, invocation *core.Invocation, args []string) error {
				client, err := f.Client(ctx, invocation)
				if err != nil {
					return err
				}
				stats, err := (async.Control{Client: client}).InspectQueues(ctx, args[1:])
				if err != nil {
					return err
				}
				return json.NewEncoder(invocation.Stdout).Encode(stats)
			}
		}})
	}
	if f.Child != nil {
		commands = append(commands, core.Command{Name: "task-child", Help: "Internal registered-task subprocess IPC entry point", Resources: append([]string(nil), f.ChildResources...), OpenResources: true, Validate: noArgs, Configure: func(*flag.FlagSet) core.Runner {
			return func(ctx context.Context, invocation *core.Invocation, _ []string) error {
				registry, err := f.Child(ctx, invocation)
				if err != nil {
					return err
				}
				if registry == nil {
					return async.ErrInvalid
				}
				return registry.ServeChild(ctx, invocation.Stdin, invocation.Stdout)
			}
		}})
	}
	return commands
}

func poll(tick func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := tick(ctx); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}
}

var ErrShutdownTimeout = errors.New("async: shutdown deadline elapsed; some handlers may still be running")

func supervise(ctx context.Context, runners []func(context.Context) error, grace time.Duration) error {
	if len(runners) == 0 || grace <= 0 {
		return async.ErrInvalid
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, len(runners))
	var wg sync.WaitGroup
	for _, runner := range runners {
		wg.Go(func() { results <- runner(child) })
	}
	var err error
	select {
	case err = <-results:
	case <-ctx.Done():
		err = ctx.Err()
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return errors.Join(err, ErrShutdownTimeout)
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}
