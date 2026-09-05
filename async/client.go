package async

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

type Route struct {
	Pattern string
	Queue   string
}
type ClientConfig struct {
	Registry       *Registry
	Broker         Broker
	Results        ResultStore
	Workflows      WorkflowStore
	Schedules      ScheduleStore
	Queues         []string
	Routes         []Route
	AllowedHeaders []string
	Authorize      func(context.Context, string, string, string) error
	Clock          func() time.Time
	NewID          func() (string, error)
}

type Client struct {
	config  ClientConfig
	queues  map[string]bool
	headers map[string]bool
}

func NewClient(config ClientConfig) (*Client, error) {
	if config.Registry == nil || config.Broker == nil || config.Results == nil {
		return nil, ErrInvalid
	}
	if err := config.Registry.Freeze(false); err != nil {
		return nil, err
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.NewID == nil {
		config.NewID = NewID
	}
	if len(config.Queues) == 0 {
		config.Queues = []string{"default"}
	}
	c := &Client{config: config, queues: make(map[string]bool), headers: make(map[string]bool)}
	for _, q := range config.Queues {
		if !namePattern.MatchString(q) {
			return nil, ErrInvalid
		}
		c.queues[q] = true
	}
	for _, route := range config.Routes {
		if _, err := path.Match(route.Pattern, "test"); err != nil || !c.queues[route.Queue] {
			return nil, ErrInvalid
		}
	}
	for _, key := range config.AllowedHeaders {
		switch strings.ToLower(key) {
		case "authorization", "cookie", "password", "secret", "token":
			return nil, ErrInvalid
		}
		if !namePattern.MatchString(key) {
			return nil, ErrInvalid
		}
		c.headers[key] = true
	}
	return c, nil
}

func (c *Client) authorize(ctx context.Context, operation, scope, id string) error {
	if c.config.Authorize != nil {
		if err := c.config.Authorize(ctx, operation, scope, id); err != nil {
			return ErrDenied
		}
		return nil
	}
	if scope != "" {
		return ErrDenied
	}
	return nil
}

func (c *Client) prepare(s Signature) (Envelope, error) {
	d, err := c.config.Registry.lookup(s.Task, s.Version)
	if err != nil {
		return Envelope{}, err
	}
	if err := d.validate(s.Args); err != nil {
		return Envelope{}, err
	}
	o := cloneJSON(s.Options)
	if o.Countdown < 0 || (!o.ETA.IsZero() && o.Countdown != 0) {
		return Envelope{}, ErrInvalid
	}
	now := c.config.Clock().UTC()
	id := o.ID
	if id == "" {
		id, err = c.config.NewID()
		if err != nil {
			return Envelope{}, err
		}
	}
	q := o.Queue
	if q == "" {
		for _, route := range c.config.Routes {
			if route.Pattern == s.Task {
				q = route.Queue
				break
			}
		}
		if q == "" {
			for _, route := range c.config.Routes {
				if match, _ := path.Match(route.Pattern, s.Task); match {
					q = route.Queue
					break
				}
			}
		}
		if q == "" {
			q = d.options.Queue
		}
	}
	if !c.queues[q] {
		return Envelope{}, ErrInvalid
	}
	for key := range o.Headers {
		if !c.headers[key] {
			return Envelope{}, ErrInvalid
		}
	}
	eta := o.ETA.UTC()
	if o.Countdown > 0 {
		eta = now.Add(o.Countdown)
	}
	if !o.ExpiresAt.IsZero() && (!now.Before(o.ExpiresAt) || (!eta.IsZero() && !eta.Before(o.ExpiresAt))) {
		return Envelope{}, ErrInvalid
	}
	e := Envelope{ProtocolVersion: ProtocolVersion, ID: id, Task: s.Task, Version: s.Version, Args: append(json.RawMessage(nil), s.Args...), CreatedAt: now, Queue: q, MaxRetries: d.options.Retry.MaxRetries, ETA: eta, ExpiresAt: o.ExpiresAt.UTC(), Priority: o.Priority, RootID: id, Scope: o.Scope, Principal: o.Principal, IdempotencyKey: o.IdempotencyKey, CorrelationID: o.CorrelationID, Headers: o.Headers, Stamps: o.Stamps}
	return e, e.Validate()
}

// AcceptanceError preserves task identity when a network outcome is uncertain.
// Retry with that same identity; generating another ID could create new work.
type AcceptanceError struct {
	ID        string
	Confirmed bool
	Cause     error
}

func (e *AcceptanceError) Error() string {
	return fmt.Sprintf("async: acceptance could not be fully confirmed for task %s", e.ID)
}
func (e *AcceptanceError) Unwrap() error { return e.Cause }

func (c *Client) Enqueue(ctx context.Context, s Signature) (Receipt, error) {
	e, err := c.prepare(s)
	if err != nil {
		return Receipt{}, err
	}
	if err := c.authorize(ctx, "enqueue", e.Scope, e.ID); err != nil {
		return Receipt{}, err
	}
	return c.publish(ctx, e)
}

func (c *Client) publish(ctx context.Context, e Envelope) (Receipt, error) {
	state := Queued
	if e.ETA.After(c.config.Clock()) {
		state = Scheduled
		if c.config.Schedules == nil {
			return Receipt{}, ErrUnavailable
		}
		if err := c.config.Schedules.Schedule(ctx, e); err != nil {
			return Receipt{}, &AcceptanceError{ID: e.ID, Cause: err}
		}
	} else if err := c.config.Broker.Publish(ctx, e); err != nil {
		return Receipt{}, &AcceptanceError{ID: e.ID, Cause: err}
	}
	if err := c.config.Results.Register(ctx, e, state); err != nil {
		return Receipt{}, &AcceptanceError{ID: e.ID, Confirmed: true, Cause: err}
	}
	return Receipt{ID: e.ID, State: state, AcceptedAt: c.config.Clock().UTC()}, nil
}

type workerContextKey struct{}
type Result[O any] struct {
	Receipt Receipt
	client  *Client
}

func RestoreResult[O any](c *Client, id string) *Result[O] {
	return &Result[O]{Receipt: Receipt{ID: id}, client: c}
}

func (r *Result[O]) Snapshot(ctx context.Context) (Record, error) {
	record, err := r.client.config.Results.Lookup(ctx, r.Receipt.ID)
	if err != nil {
		return Record{}, err
	}
	if err := r.client.authorize(ctx, "read", record.Envelope.Scope, r.Receipt.ID); err != nil {
		return Record{}, err
	}
	return record, nil
}
func (r *Result[O]) Ready(ctx context.Context) (bool, error) {
	record, err := r.Snapshot(ctx)
	return record.State.Terminal(), err
}
func (r *Result[O]) Successful(ctx context.Context) (bool, error) {
	record, err := r.Snapshot(ctx)
	return record.State == Succeeded, err
}
func (r *Result[O]) Failed(ctx context.Context) (bool, error) {
	record, err := r.Snapshot(ctx)
	return record.State == Failed, err
}

func (r *Result[O]) Get(ctx context.Context) (O, error) {
	var out O
	if ctx.Value(workerContextKey{}) != nil {
		return out, ErrWorkerJoin
	}
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	for {
		record, err := r.Snapshot(ctx)
		if err != nil {
			return out, err
		}
		if record.State.Terminal() {
			if record.Failure != nil {
				return out, *record.Failure
			}
			if record.State != Succeeded {
				return out, Failure{Code: string(record.State), Message: "Task did not succeed"}
			}
			if len(record.Output) == 0 {
				return out, ErrResultExpired
			}
			return out, decodeJSON(record.Output, &out)
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-timer.C:
		}
	}
}
func (r *Result[O]) Wait(ctx context.Context) (O, error) { return r.Get(ctx) }
func (r *Result[O]) Forget(ctx context.Context) error {
	record, err := r.Snapshot(ctx)
	if err != nil {
		return err
	}
	if err := r.client.authorize(ctx, "forget", record.Envelope.Scope, r.Receipt.ID); err != nil {
		return err
	}
	return r.client.config.Results.Forget(ctx, r.Receipt.ID)
}
func (r *Result[O]) Revoke(ctx context.Context) error {
	record, err := r.Snapshot(ctx)
	if err != nil {
		return err
	}
	if err := r.client.authorize(ctx, "revoke", record.Envelope.Scope, r.Receipt.ID); err != nil {
		return err
	}
	return r.client.config.Results.RequestCancel(ctx, r.Receipt.ID, record.Envelope.Scope)
}
