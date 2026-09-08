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
	Registry  *Registry
	Broker    Broker
	Results   ResultStore
	Workflows WorkflowStore
	Schedules ScheduleStore
	Presence  WorkerPresenceStore
	Controls  WorkerControlStore
	// Events observes confirmed queue/schedule acceptance, never execution
	// authority. OnEventError receives safe errors and must return promptly.
	Events         EventSink
	OnEventError   func(error)
	EventTimeout   time.Duration
	Queues         []string
	Routes         []Route
	AllowedHeaders []string
	Authorize      func(context.Context, string, string, string) error
	Clock          func() time.Time
	NewID          func() (string, error)
	PublishRetry   *PublishRetryPolicy
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
	var retryErr error
	config.PublishRetry, retryErr = normalizePublishRetry(config.PublishRetry)
	if retryErr != nil {
		return nil, retryErr
	}
	if config.EventTimeout == 0 {
		config.EventTimeout = time.Second
	}
	if config.EventTimeout < time.Millisecond || config.EventTimeout > 30*time.Second {
		return nil, ErrInvalid
	}
	// Freeze routing and metadata allowlists at construction. The caller may
	// reuse or edit its configuration slices without racing future dispatches.
	config.Queues = append([]string(nil), config.Queues...)
	config.Routes = append([]Route(nil), config.Routes...)
	config.AllowedHeaders = append([]string(nil), config.AllowedHeaders...)
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
	var normalizeErr error
	s, normalizeErr = c.normalizeLinkOptions(s, s.Options.Scope, 0)
	if normalizeErr != nil {
		return Envelope{}, normalizeErr
	}
	d, err := c.config.Registry.lookup(s.Task, s.Version)
	if err != nil {
		return Envelope{}, err
	}
	if err := c.config.Registry.validateLinks(s, 0); err != nil {
		return Envelope{}, err
	}
	if err := d.validate(s.Args); err != nil {
		return Envelope{}, err
	}
	if len(s.Callbacks) > 32 || len(s.Errbacks) > 32 {
		return Envelope{}, ErrInvalid
	}
	for _, link := range append(append([]Signature(nil), s.Callbacks...), s.Errbacks...) {
		linked, err := c.config.Registry.lookup(link.Task, link.Version)
		if err != nil {
			return Envelope{}, err
		}
		if err := linked.validate(link.Args); err != nil {
			return Envelope{}, err
		}
		if link.Options.Scope != "" && link.Options.Scope != s.Options.Scope {
			return Envelope{}, ErrDenied
		}
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
	e.Callbacks = cloneJSON(s.Callbacks)
	e.Errbacks = cloneJSON(s.Errbacks)
	return e, e.Validate()
}

func (c *Client) normalizeLinkOptions(s Signature, scope string, depth int) (Signature, error) {
	if depth > 16 {
		return Signature{}, ErrInvalid
	}
	s = s.Clone()
	for kind, links := range [][]Signature{s.Callbacks, s.Errbacks} {
		for i, link := range links {
			if link.Options.Scope != "" && link.Options.Scope != scope {
				return Signature{}, ErrDenied
			}
			link.Options.Scope = scope
			d, err := c.config.Registry.lookup(link.Task, link.Version)
			if err != nil {
				return Signature{}, err
			}
			if link.Options.Queue == "" {
				for _, route := range c.config.Routes {
					if route.Pattern == link.Task {
						link.Options.Queue = route.Queue
						break
					}
				}
				if link.Options.Queue == "" {
					for _, route := range c.config.Routes {
						if match, _ := path.Match(route.Pattern, link.Task); match {
							link.Options.Queue = route.Queue
							break
						}
					}
				}
				if link.Options.Queue == "" {
					link.Options.Queue = d.options.Queue
				}
			}
			if !c.queues[link.Options.Queue] || link.Options.Priority < 0 || link.Options.Priority > 9 || link.Options.Countdown < 0 {
				return Signature{}, ErrInvalid
			}
			for key := range link.Options.Headers {
				if !c.headers[key] {
					return Signature{}, ErrInvalid
				}
			}
			links[i], err = c.normalizeLinkOptions(link, scope, depth+1)
			if err != nil {
				return Signature{}, err
			}
		}
		if kind == 0 {
			s.Callbacks = links
		} else {
			s.Errbacks = links
		}
	}
	return s, nil
}

// AcceptanceError preserves task identity when a network outcome is uncertain.
// Retry with that same identity; generating another ID could create new work.
type AcceptanceError struct {
	ID        string
	Confirmed bool
	Cause     error
	envelope  Envelope
	state     State
	accepted  bool
}

func (e *AcceptanceError) Error() string {
	return fmt.Sprintf("async: acceptance could not be fully confirmed for task %s", e.ID)
}
func (e *AcceptanceError) Unwrap() error { return e.Cause }

// Retry replays the exact accepted-or-uncertain envelope, including its original
// creation time and converted countdown. Rebuilding a signature can change those
// immutable dispatch inputs and is not equivalent to replaying the same request.
func (e *AcceptanceError) Retry(ctx context.Context, c *Client) (Receipt, error) {
	if e == nil {
		return Receipt{}, ErrInvalid
	}
	return runProducer(ctx, c, nil, e)
}

func (c *Client) Enqueue(ctx context.Context, s Signature) (Receipt, error) {
	return runProducer(ctx, c, &s, nil)
}

func (c *Client) publish(ctx context.Context, e Envelope) (Receipt, error) {
	state := Queued
	if e.ETA.After(c.config.Clock()) {
		state = Scheduled
		if c.config.Schedules == nil {
			return Receipt{}, ErrUnavailable
		}
		if err := c.config.Schedules.Schedule(ctx, e); err != nil {
			return Receipt{}, &AcceptanceError{ID: e.ID, Cause: err, envelope: copyProducerEnvelope(e), state: state}
		}
	} else if err := c.config.Broker.Publish(ctx, e); err != nil {
		return Receipt{}, &AcceptanceError{ID: e.ID, Cause: err, envelope: copyProducerEnvelope(e), state: state}
	}
	if err := c.config.Results.Register(ctx, e, state); err != nil {
		return Receipt{}, &AcceptanceError{ID: e.ID, Confirmed: true, Cause: err, envelope: copyProducerEnvelope(e), state: state, accepted: true}
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
