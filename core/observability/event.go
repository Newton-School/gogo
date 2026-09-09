// Package observability provides explicitly owned, bounded operation telemetry.
// It does not automatically instrument HTTP requests, database calls or tasks.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"sync/atomic"
	"time"
)

var (
	ErrInvalidConfig  = errors.New("invalid telemetry configuration")
	ErrInvalidContext = errors.New("invalid telemetry context")
	ErrInvalidEvent   = errors.New("invalid telemetry operation")
	ErrClosed         = errors.New("telemetry pipeline is closing or closed")
	ErrBusy           = errors.New("telemetry queue is full")
	ErrExporter       = errors.New("telemetry exporter failed")
)

// ID and SpanID are fixed-width opaque correlation identifiers, not user data.
// They are not metric labels. No header, baggage or payload parsing is implicit.
type ID [16]byte
type SpanID [8]byte

func (id ID) String() string     { return hex.EncodeToString(id[:]) }
func (id SpanID) String() string { return hex.EncodeToString(id[:]) }

type Correlation struct {
	TraceID   ID
	SpanID    SpanID
	RequestID ID
	TaskID    ID
}

type correlationKey struct{}

// WithCorrelation explicitly propagates trusted, opaque identifiers. Zero fields
// mean absent; Start creates a trace when absent and always creates a child span.
func WithCorrelation(ctx context.Context, ids Correlation) (result context.Context, err error) {
	if _, err = inspectContext(ctx); err != nil {
		return nil, err
	}
	defer func() {
		if recover() != nil {
			result, err = nil, ErrInvalidContext
		}
	}()
	return context.WithValue(ctx, correlationKey{}, ids), nil
}

// CorrelationFromContext returns a value copy. Malformed contexts are refused.
func CorrelationFromContext(ctx context.Context) (ids Correlation, ok bool) {
	defer func() {
		if recover() != nil {
			ids, ok = Correlation{}, false
		}
	}()
	if _, err := inspectContext(ctx); err != nil {
		return Correlation{}, false
	}
	ids, ok = ctx.Value(correlationKey{}).(Correlation)
	if _, err := inspectContext(ctx); err != nil {
		return Correlation{}, false
	}
	return ids, ok
}

// Status is a finite, coarse outcome vocabulary; error text is never accepted.
type Status uint8

const (
	StatusUnknown Status = iota
	StatusSuccess
	StatusFailure
	StatusCanceled
	StatusHTTP1xx
	StatusHTTP2xx
	StatusHTTP3xx
	StatusHTTP4xx
	StatusHTTP5xx
)

func (s Status) String() string {
	switch s {
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failure"
	case StatusCanceled:
		return "canceled"
	case StatusHTTP1xx:
		return "1xx"
	case StatusHTTP2xx:
		return "2xx"
	case StatusHTTP3xx:
		return "3xx"
	case StatusHTTP4xx:
		return "4xx"
	case StatusHTTP5xx:
		return "5xx"
	default:
		return "unknown"
	}
}

func HTTPStatus(code int) Status {
	if code < 100 || code > 599 {
		return StatusUnknown
	}
	return Status(int(StatusHTTP1xx) + code/100 - 1)
}

const MaxDuration = 24 * time.Hour

// Event is a read-only value projection. Its names come from the configured
// allowlists; there are no arbitrary attributes, context values or error causes.
type Event struct {
	operation, route string
	correlation      Correlation
	parent           SpanID
	status           Status
	duration         time.Duration
}

func (e Event) Operation() string        { return e.operation }
func (e Event) Route() string            { return e.route }
func (e Event) Correlation() Correlation { return e.correlation }
func (e Event) ParentSpanID() SpanID     { return e.parent }
func (e Event) Status() Status           { return e.status }
func (e Event) Duration() time.Duration  { return e.duration }

// Span must not be copied. End is safe to call concurrently and records once.
// It retains no caller context. Duration uses Go's native monotonic clock.
type Span struct {
	pipeline *Pipeline
	event    Event
	started  time.Time
	sampled  bool
	ended    atomic.Bool
}

// Start validates registered names and creates explicit child correlation. Empty
// route is permitted for operations that have no HTTP route. Sampling is stable
// for the trace ID within this pipeline; it does not import remote sample flags.
func (p *Pipeline) Start(ctx context.Context, operation, route string) (context.Context, *Span, error) {
	if p == nil || p.queue == nil {
		return nil, nil, ErrClosed
	}
	if len(operation) > maxOperationBytes || len(route) > maxRouteBytes {
		return nil, nil, ErrInvalidEvent
	}
	op, ok := p.operations[operation]
	if !ok {
		return nil, nil, ErrInvalidEvent
	}
	var canonicalRoute string
	if route != "" {
		canonicalRoute, ok = p.routes[route]
		if !ok {
			return nil, nil, ErrInvalidEvent
		}
	}
	p.mu.Lock()
	closing := p.closing
	p.mu.Unlock()
	if closing {
		return nil, nil, ErrClosed
	}
	if _, err := inspectContext(ctx); err != nil {
		return nil, nil, err
	}
	ids, err := readCorrelation(ctx)
	if err != nil {
		return nil, nil, err
	}
	parent := ids.SpanID
	if ids.TraceID == (ID{}) {
		parent = SpanID{}
		if _, err := rand.Read(ids.TraceID[:]); err != nil {
			return nil, nil, ErrInvalidEvent
		}
	}
	if _, err := rand.Read(ids.SpanID[:]); err != nil {
		return nil, nil, ErrInvalidEvent
	}
	next, err := WithCorrelation(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	sampled := p.sampleRate == SampleAll || (p.sampleRate != 0 && binary.BigEndian.Uint64(ids.TraceID[:8])%uint64(SampleAll) < uint64(p.sampleRate))
	return next, &Span{pipeline: p, event: Event{operation: op, route: canonicalRoute, correlation: ids, parent: parent}, started: time.Now(), sampled: sampled}, nil
}

// End never calls an exporter or waits for queue capacity. It reports whether an
// event was admitted, not delivered. Duplicates return false without recounting.
func (s *Span) End(status Status) bool {
	if s == nil || s.pipeline == nil || !s.ended.CompareAndSwap(false, true) {
		return false
	}
	p := s.pipeline
	if status > StatusHTTP5xx {
		p.dropped.Add(1)
		return false
	}
	if !s.sampled {
		p.sampledOut.Add(1)
		return false
	}
	event := s.event
	event.status = status
	event.duration = time.Since(s.started)
	if event.duration < 0 {
		event.duration = 0
	}
	if event.duration > MaxDuration {
		event.duration = MaxDuration
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		p.dropped.Add(1)
		return false
	}
	select {
	case p.queue <- queueItem{event: event}:
		return true
	default:
		p.dropped.Add(1)
		return false
	}
}

func readCorrelation(ctx context.Context) (ids Correlation, err error) {
	defer func() {
		if recover() != nil {
			ids, err = Correlation{}, ErrInvalidContext
		}
	}()
	ids, _ = ctx.Value(correlationKey{}).(Correlation)
	return ids, nil
}

type contextInfo struct {
	done        <-chan struct{}
	deadline    time.Time
	hasDeadline bool
}

func inspectContext(ctx context.Context) (info contextInfo, err error) {
	defer func() {
		if recover() != nil {
			info, err = contextInfo{}, ErrInvalidContext
		}
	}()
	if nilInterface(ctx) {
		return contextInfo{}, ErrInvalidContext
	}
	err = ctx.Err()
	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return contextInfo{}, err
		}
		return contextInfo{}, ErrInvalidContext
	}
	info.deadline, info.hasDeadline = ctx.Deadline()
	info.done = ctx.Done()
	select {
	case <-info.done:
		err = ctx.Err()
		if err == context.Canceled || err == context.DeadlineExceeded {
			return contextInfo{}, err
		}
		return contextInfo{}, ErrInvalidContext
	default:
	}
	err = ctx.Err()
	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return contextInfo{}, err
		}
		return contextInfo{}, ErrInvalidContext
	}
	return info, nil
}

func canceledContextError(ctx context.Context) error {
	_, err := inspectContext(ctx)
	if err == nil {
		return ErrInvalidContext
	}
	return err
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
