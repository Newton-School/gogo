package main

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/gogo-services/apps/catalog"
	"example.com/gogo-services/internal/clients"
	"example.com/gogo-services/internal/database"
	"example.com/gogo-services/internal/taskqueue"
	"flag"
	"github.com/Newton-School/gogo/core/management"
	"time"
)

func reportCommand(database *database.Connection, queue *taskqueue.Connection) management.Command {
	return management.Command{Name: "report", Help: "Read shared data, call the API and dispatch a report task", Resources: []string{"database", "redis"}, OpenResources: true,
		Validate: func(args []string) error {
			if len(args) != 0 {
				return errors.New("report takes flags only")
			}
			return nil
		},
		Configure: func(flags *flag.FlagSet) management.Runner {
			apiURL := flags.String("api-url", "http://127.0.0.1:8000", "operator-configured public demonstration API origin")
			return func(ctx context.Context, i *management.Invocation, _ []string) error {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				local, err := catalog.Count(ctx, database.Store)
				if err != nil {
					return errors.New("database report failed")
				}
				remote, err := clients.Count(ctx, *apiURL)
				if err != nil {
					return err
				}
				runtime, err := queue.Runtime()
				if err != nil {
					return err
				}
				result, err := runtime.Double.Delay(ctx, runtime.Client, local)
				if err != nil {
					return errors.New("task submission failed")
				}
				// Print the receipt before waiting so an interrupted caller can inspect
				// the accepted task instead of unknowingly publishing it again.
				if err := json.NewEncoder(i.Stdout).Encode(map[string]any{"task_id": result.Receipt.ID, "database_count": local, "api_count": remote}); err != nil {
					return err
				}
				value, err := result.Get(ctx)
				if err != nil {
					return errors.New("task result unavailable; inspect the printed task receipt")
				}
				return json.NewEncoder(i.Stdout).Encode(map[string]int64{"doubled_count": value})
			}
		},
	}
}
