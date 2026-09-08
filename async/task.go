package async

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Newton-School/gogo/core/ratelimit"
)

type TaskContext struct {
	ID               string
	Retries          int
	DeliveryCount    int64
	Fence            uint64
	WorkerID         string
	WorkflowID       string
	ParentID         string
	RootID           string
	Scope            string
	Principal        string
	Headers          map[string]string
	Stamps           map[string]string
	ReplacementDepth int
	progress         func(context.Context, json.RawMessage) error
}

func (t TaskContext) Progress(ctx context.Context, value any) error {
	b, err := json.Marshal(value)
	if err != nil || len(b) > 16<<10 {
		return ErrInvalid
	}
	if t.progress == nil {
		return ErrUnavailable
	}
	return t.progress(ctx, b)
}
func (t TaskContext) Retry(err error) error       { return Retry(err) }
func (t TaskContext) Replace(canvas Canvas) error { return Replace(canvas) }

type Hooks struct {
	BeforeStart func(context.Context, TaskContext) error
	OnSuccess   func(context.Context, TaskContext, json.RawMessage) error
	OnFailure   func(context.Context, TaskContext, error) error
	OnRetry     func(context.Context, TaskContext, error) error
	AfterReturn func(context.Context, TaskContext, State) error
}

type TaskOptions struct {
	Queue           string
	Retry           RetryPolicy
	SoftLimit       time.Duration
	HardLimit       time.Duration
	Hooks           Hooks
	Authorize       func(context.Context, TaskContext) error
	ValidatePayload func(json.RawMessage) error
	// PerWorkerConcurrency is a local task-type quota, not a distributed lock.
	// Zero uses the worker's global bound.
	PerWorkerConcurrency int
	Rate                 *TaskRate
}

type TaskRate struct {
	Limit ratelimit.Limit
	// Scope is explicitly "worker" or "distributed". Distributed admission
	// requires Worker.RateLimiter; there is never a silent local fallback.
	Scope string
}

type Handler[I, O any] func(context.Context, TaskContext, I) (O, error)

type definition struct {
	name           string
	version        int
	input, output  reflect.Type
	validate       func(json.RawMessage) error
	validateOutput func(json.RawMessage) error
	call           func(context.Context, TaskContext, json.RawMessage) (json.RawMessage, error)
	options        TaskOptions
}

type Registry struct {
	mu          sync.RWMutex
	definitions map[string]*definition
	frozen      bool
}

func NewRegistry() *Registry                        { return &Registry{definitions: make(map[string]*definition)} }
func definitionKey(name string, version int) string { return fmt.Sprintf("%s@%d", name, version) }

func Register[I, O any](r *Registry, name string, version int, handler Handler[I, O], options TaskOptions) (*Task[I, O], error) {
	if r == nil || !namePattern.MatchString(name) || strings.HasPrefix(name, "gogo.") || version < 1 || options.SoftLimit < 0 || options.HardLimit < 0 || (options.HardLimit > 0 && options.SoftLimit > options.HardLimit) || options.PerWorkerConcurrency < 0 || options.PerWorkerConcurrency > 1024 {
		return nil, ErrInvalid
	}
	if options.Rate != nil {
		rate := *options.Rate
		if rate.Limit.Validate(1) != nil || rate.Scope != "worker" && rate.Scope != "distributed" {
			return nil, ErrInvalid
		}
		options.Rate = &rate
	}
	if options.Queue == "" {
		options.Queue = "default"
	}
	if !namePattern.MatchString(options.Queue) {
		return nil, ErrInvalid
	}
	policy, err := options.Retry.normalized()
	if err != nil {
		return nil, err
	}
	options.Retry = policy
	d := &definition{name: name, version: version, input: reflect.TypeFor[I](), output: reflect.TypeFor[O](), options: options}
	d.validate = func(raw json.RawMessage) error {
		if options.ValidatePayload != nil {
			if err := options.ValidatePayload(raw); err != nil {
				return err
			}
		}
		var input I
		if err := decodeJSON(raw, &input); err != nil {
			return ErrInvalid
		}
		if v, ok := any(input).(interface{ Validate() error }); ok {
			return v.Validate()
		}
		if v, ok := any(&input).(interface{ Validate() error }); ok {
			return v.Validate()
		}
		return nil
	}
	d.validateOutput = func(raw json.RawMessage) error {
		var value O
		if len(raw) > MaxPayloadBytes {
			return ErrInvalid
		}
		if err := decodeJSON(raw, &value); err != nil {
			return ErrInvalid
		}
		if validator, ok := any(value).(interface{ Validate() error }); ok {
			return validator.Validate()
		}
		if validator, ok := any(&value).(interface{ Validate() error }); ok {
			return validator.Validate()
		}
		return nil
	}
	if handler != nil {
		d.call = func(ctx context.Context, tc TaskContext, raw json.RawMessage) (json.RawMessage, error) {
			var input I
			if err := decodeJSON(raw, &input); err != nil {
				return nil, ErrInvalid
			}
			out, err := handler(ctx, tc, input)
			if err != nil {
				return nil, err
			}
			b, err := json.Marshal(out)
			if err != nil || len(b) > MaxPayloadBytes {
				return nil, ErrInvalid
			}
			if err := d.validateOutput(b); err != nil {
				return nil, ErrInvalid
			}
			return b, nil
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil, ErrFrozen
	}
	if r.definitions == nil {
		r.definitions = make(map[string]*definition)
	}
	key := definitionKey(name, version)
	if _, exists := r.definitions[key]; exists {
		return nil, ErrConflict
	}
	r.definitions[key] = d
	return &Task[I, O]{definition: d}, nil
}

func (r *Registry) Freeze(worker bool) error {
	if r == nil {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if worker {
		for _, d := range r.definitions {
			if d.call == nil {
				return fmt.Errorf("%w: %s", ErrUnknownTask, d.name)
			}
		}
	}
	r.frozen = true
	return nil
}
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (r *Registry) lookup(name string, version int) (*definition, error) {
	if r == nil {
		return nil, ErrUnknownTask
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.definitions[definitionKey(name, version)]
	if !ok {
		return nil, ErrUnknownTask
	}
	return d, nil
}

type Task[I, O any] struct{ definition *definition }

func (t *Task[I, O]) Name() string { return t.definition.name }
func (t *Task[I, O]) Signature(args I) (Signature, error) {
	b, err := json.Marshal(args)
	if err != nil || len(b) > MaxPayloadBytes {
		return Signature{}, ErrInvalid
	}
	if err := t.definition.validate(b); err != nil {
		return Signature{}, err
	}
	return Signature{Task: t.definition.name, Version: t.definition.version, Args: b}, nil
}
func (t *Task[I, O]) Delay(ctx context.Context, c *Client, args I, options ...DispatchOption) (result *Result[O], err error) {
	started := time.Now()
	defer func() {
		if recover() != nil {
			result, err = nil, ErrUnavailable
		}
	}()
	if t == nil || t.definition == nil || c == nil {
		return nil, ErrInvalid
	}
	// Freeze handles before argument encoding, validation or dispatch-option
	// callbacks. The returned result must retain the client that accepted it.
	task, client := *t, *c
	options = append([]DispatchOption(nil), options...)
	if err := publishContextError(ctx); err != nil {
		return nil, err
	}
	s, err := task.Signature(args)
	if err != nil {
		return nil, err
	}
	s = s.Set(options...)
	receipt, err := runProducerStarted(ctx, &client, &s, nil, started)
	if err != nil {
		return nil, err
	}
	return &Result[O]{Receipt: receipt, client: &client}, nil
}

// Apply is explicit eager execution through the same JSON validation path. It
// does not silently substitute for an unavailable broker or durable result store.
func (t *Task[I, O]) Apply(ctx context.Context, args I) (O, error) {
	var out O
	s, err := t.Signature(args)
	if err != nil {
		return out, err
	}
	if t.definition.call == nil {
		return out, ErrUnknownTask
	}
	id, err := NewID()
	if err != nil {
		return out, err
	}
	tc := TaskContext{ID: id}
	if t.definition.options.Authorize != nil {
		if err := t.definition.options.Authorize(ctx, tc); err != nil {
			return out, ErrDenied
		}
	}
	b, err := t.definition.call(ctx, tc, s.Args)
	if err != nil {
		return out, err
	}
	err = decodeJSON(b, &out)
	return out, err
}

type DispatchOptions struct {
	ID             string            `json:"id,omitempty"`
	Queue          string            `json:"queue,omitempty"`
	Priority       int               `json:"priority,omitempty"`
	ETA            time.Time         `json:"eta,omitempty"`
	Countdown      time.Duration     `json:"countdown,omitempty"`
	ExpiresAt      time.Time         `json:"expires_at,omitempty"`
	Scope          string            `json:"scope,omitempty"`
	Principal      string            `json:"principal,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	CorrelationID  string            `json:"correlation_id,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Stamps         map[string]string `json:"stamps,omitempty"`
}
type DispatchOption func(*DispatchOptions)

func OnQueue(queue string) DispatchOption { return func(o *DispatchOptions) { o.Queue = queue } }
func WithID(id string) DispatchOption     { return func(o *DispatchOptions) { o.ID = id } }
func WithPriority(priority int) DispatchOption {
	return func(o *DispatchOptions) { o.Priority = priority }
}
func WithETA(eta time.Time) DispatchOption { return func(o *DispatchOptions) { o.ETA = eta } }
func WithCountdown(delay time.Duration) DispatchOption {
	return func(o *DispatchOptions) { o.Countdown = delay }
}
func WithExpiry(expires time.Time) DispatchOption {
	return func(o *DispatchOptions) { o.ExpiresAt = expires }
}
func WithScope(scope, principal string) DispatchOption {
	return func(o *DispatchOptions) { o.Scope = scope; o.Principal = principal }
}
func WithHeaders(headers map[string]string) DispatchOption {
	return func(o *DispatchOptions) { o.Headers = cloneJSON(headers) }
}
func WithStamps(stamps map[string]string) DispatchOption {
	return func(o *DispatchOptions) { o.Stamps = cloneJSON(stamps) }
}

type Signature struct {
	Task        string          `json:"task"`
	Version     int             `json:"version"`
	Args        json.RawMessage `json:"args"`
	Options     DispatchOptions `json:"options"`
	Immutable   bool            `json:"immutable,omitempty"`
	ParentField string          `json:"parent_field,omitempty"`
	Callbacks   []Signature     `json:"callbacks,omitempty"`
	Errbacks    []Signature     `json:"errbacks,omitempty"`
}

func (s Signature) Link(callback Signature) Signature {
	s = s.Clone()
	s.Callbacks = append(s.Callbacks, callback.Clone())
	return s
}
func (s Signature) LinkError(callback Signature) Signature {
	s = s.Clone()
	s.Errbacks = append(s.Errbacks, callback.Clone())
	return s
}

func (s Signature) Clone() Signature { return cloneJSON(s) }
func (s Signature) Set(options ...DispatchOption) Signature {
	s = s.Clone()
	for _, option := range options {
		if option != nil {
			option(&s.Options)
		}
	}
	return s
}
func (s Signature) ImmutableSignature() Signature { s = s.Clone(); s.Immutable = true; return s }

// FromParent binds prior output into a named JSON field. An empty field replaces
// the entire input. Composition checks the declared Go types before dispatch.
func (s Signature) FromParent(field string) Signature {
	s = s.Clone()
	s.ParentField = field
	s.Immutable = false
	return s
}

func (s Signature) bind(result json.RawMessage) (Signature, error) {
	s = s.Clone()
	if s.Immutable {
		return s, nil
	}
	if s.ParentField == "" {
		s.Args = append(json.RawMessage(nil), result...)
		return s, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(s.Args, &fields); err != nil || fields == nil {
		return s, ErrInvalid
	}
	fields[s.ParentField] = result
	b, err := json.Marshal(fields)
	s.Args = b
	return s, err
}
