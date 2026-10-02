package taskqueue

import (
	"context"
	"errors"
	"example.com/gogo-services/apps/reports"
	"github.com/Newton-School/gogo/async"
	asyncredis "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"slices"
)

type Connection struct{ Tasks, Results *connector.Connection }

func (c *Connection) Resources(settings conf.Values, names []string) ([]app.Resource, error) {
	if !slices.Contains(names, "redis") {
		return nil, nil
	}
	var resources []app.Resource
	for _, role := range []connector.Role{connector.TaskRole, connector.ResultRole} {
		resources = append(resources, app.Resource{Name: "redis_" + string(role), Open: func(ctx context.Context) (func(context.Context) error, error) {
			connection, err := connector.Open(ctx, connector.Config{URL: settings.Secret("GOGO_REDIS_URL").Reveal(), Role: role, Development: settings.String("GOGO_ENV") != "production", Production: settings.String("GOGO_ENV") == "production", Timeout: settings.Duration("GOGO_REDIS_OPERATION_TIMEOUT")})
			if err != nil {
				return nil, errors.New("queue connection failed")
			}
			if role == connector.TaskRole {
				c.Tasks = connection
			} else {
				c.Results = connection
			}
			return func(context.Context) error { return connection.Close() }, nil
		}})
	}
	return resources, nil
}

type Runtime struct {
	Registry  *async.Registry
	Double    *async.Task[int64, int64]
	Client    *async.Client
	Broker    *asyncredis.Broker
	Results   *asyncredis.Results
	Workflows *asyncredis.Workflows
}

func (c *Connection) Runtime() (*Runtime, error) {
	registry, task, err := reports.Tasks()
	if err != nil {
		return nil, err
	}
	broker := &asyncredis.Broker{Connection: c.Tasks, Queues: []string{reports.Queue}}
	results := &asyncredis.Results{Connection: c.Results}
	workflows := &asyncredis.Workflows{Connection: c.Results}
	schedules := &asyncredis.Schedules{Connection: c.Results}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Schedules: schedules, Queues: []string{reports.Queue}})
	if err != nil {
		return nil, err
	}
	return &Runtime{Registry: registry, Double: task, Client: client, Broker: broker, Results: results, Workflows: workflows}, nil
}
