package observability

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	SampleAll         uint32 = 1_000_000
	maxOperationBytes        = 96
	maxRouteBytes            = 256
	maxOperations            = 256
	maxRoutes                = 4096
	MaxQueueSize             = 4096
)

// Config is copied by New. Names are developer-declared static identifiers, never
// runtime request paths, user IDs, SQL, credentials or payload values. Sampling is
// parts per million: zero disables emission; SampleAll records every ended span.
type Config struct {
	Operations      []string
	Routes          []string
	QueueSize       int
	SampleRate      uint32
	ExportTimeout   time.Duration
	ShutdownTimeout time.Duration
}

// Exporter is owned by one Pipeline, which invokes methods serially. Implementors
// must cooperate with context cancellation, bound their own buffers and not call
// Flush or Close on the owning pipeline. Go cannot terminate a stuck callback.
// Events and contexts contain no arbitrary request context values. A nil error
// means accepted by the exporter, not durable or remote delivery.
type Exporter interface {
	Export(context.Context, Event) error
	Flush(context.Context) error
	Close(context.Context) error
}

// Stats contains local process counters, not labels. Sampled-out spans are not
// dropped events. Dropped includes queue overflow, closed admission, invalid End
// status, failed exports and records abandoned after shutdown cancellation.
type Stats struct{ Exported, Dropped, SampledOut, ExportFailures uint64 }

type flushRequest struct {
	ctx  context.Context
	done chan error
}
type queueItem struct {
	event Event
	flush *flushRequest
}
type closeRequest struct{ ctx context.Context }

// Pipeline has a fixed queue and one worker. Do not copy it. Close is explicit;
// creating a pipeline is not a global registration or an application hook.
type Pipeline struct {
	operations, routes                            map[string]string
	sampleRate                                    uint32
	exportTimeout, shutdownTimeout                time.Duration
	exporter                                      Exporter
	queue                                         chan queueItem
	closeRequest                                  chan closeRequest
	done                                          chan struct{}
	workContext                                   context.Context
	cancelWork                                    context.CancelFunc
	mu                                            sync.Mutex
	closing                                       bool
	closeErr                                      error
	exported, dropped, sampledOut, exportFailures atomic.Uint64
}

func New(config Config, exporter Exporter) (*Pipeline, error) {
	// Snapshot all caller-owned slices and strings before any provider invocation.
	if len(config.Operations) == 0 || len(config.Operations) > maxOperations || len(config.Routes) > maxRoutes {
		return nil, ErrInvalidConfig
	}
	ops, err := names(config.Operations, false)
	if err != nil {
		return nil, err
	}
	routes, err := names(config.Routes, true)
	if err != nil {
		return nil, err
	}
	if config.QueueSize == 0 {
		config.QueueSize = 256
	}
	if config.ExportTimeout == 0 {
		config.ExportTimeout = time.Second
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = 30 * time.Second
	}
	if config.QueueSize < 1 || config.QueueSize > MaxQueueSize || config.SampleRate > SampleAll || config.ExportTimeout < 1 || config.ExportTimeout > time.Minute || config.ShutdownTimeout < 1 || config.ShutdownTimeout > time.Minute || nilInterface(exporter) {
		return nil, ErrInvalidConfig
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{operations: ops, routes: routes, sampleRate: config.SampleRate, exportTimeout: config.ExportTimeout, shutdownTimeout: config.ShutdownTimeout, exporter: exporter, queue: make(chan queueItem, config.QueueSize), closeRequest: make(chan closeRequest, 1), done: make(chan struct{}), workContext: ctx, cancelWork: cancel}
	go p.run()
	return p, nil
}

func names(values []string, route bool) (map[string]string, error) {
	result := make(map[string]string, len(values))
	limit := maxOperationBytes
	if route {
		limit = maxRouteBytes
	}
	for _, name := range values {
		if name == "" || len(name) > limit || (route && name[0] != '/') {
			return nil, ErrInvalidConfig
		}
		for _, b := range []byte(name) {
			if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == '.' || b == ':' {
				continue
			}
			if route && (b == '/' || b == '<' || b == '>' || b == '{' || b == '}' || b == '*') {
				continue
			}
			return nil, ErrInvalidConfig
		}
		if _, exists := result[name]; exists {
			return nil, ErrInvalidConfig
		}
		owned := strings.Clone(name)
		result[owned] = owned
	}
	return result, nil
}

func (p *Pipeline) Stats() Stats {
	if p == nil {
		return Stats{}
	}
	return Stats{Exported: p.exported.Load(), Dropped: p.dropped.Load(), SampledOut: p.sampledOut.Load(), ExportFailures: p.exportFailures.Load()}
}

// Flush places one barrier in the same bounded queue. It never waits for queue
// capacity: ErrBusy means no barrier was admitted. Success means prior admitted
// records finished their export attempts and the exporter flush succeeded, not
// that dropped/failed records were delivered; Stats preserves those outcomes.
// Concurrent later End calls are not covered. Only a clean, bounded context
// is queued; the caller's context and values are not retained by the worker.
func (p *Pipeline) Flush(ctx context.Context) error {
	if p == nil || p.queue == nil {
		return ErrClosed
	}
	info, err := inspectContext(ctx)
	if err != nil {
		return err
	}
	clean, cancel := cleanContext(info, p.shutdownTimeout)
	defer cancel()
	request := &flushRequest{ctx: clean, done: make(chan error, 1)}
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return ErrClosed
	}
	select {
	case p.queue <- queueItem{flush: request}:
		p.mu.Unlock()
	default:
		p.mu.Unlock()
		return ErrBusy
	}
	return await(ctx, info.done, clean, request.done)
}

// Close stops admission once. The first caller owns the bounded cleanup context;
// later callers only wait. Timeout bounds the caller, not a non-cooperative
// exporter: at most one worker/callback may remain until that callback returns.
// Once it returns, remaining records are dropped and cleanup is attempted once.
func (p *Pipeline) Close(ctx context.Context) error {
	if p == nil || p.queue == nil {
		return ErrClosed
	}
	info, err := inspectContext(ctx)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return err
	}
	if err == context.DeadlineExceeded {
		info.deadline, info.hasDeadline = time.Now().Add(-time.Nanosecond), true
	}
	clean, cancel := cleanContext(info, p.shutdownTimeout)
	defer cancel()
	if err == context.Canceled {
		cancel()
	}
	p.mu.Lock()
	if !p.closing {
		p.closing = true
		context.AfterFunc(clean, p.cancelWork)
		p.closeRequest <- closeRequest{ctx: clean}
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		return p.closeErr
	default:
	}
	select {
	case <-p.done:
		return p.closeErr
	case <-info.done:
		return canceledContextError(ctx)
	case <-clean.Done():
		if err != nil {
			return err
		}
		return clean.Err()
	}
}

func cleanContext(info contextInfo, maximum time.Duration) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(maximum)
	if info.hasDeadline && info.deadline.Before(deadline) {
		deadline = info.deadline
	}
	return context.WithDeadline(context.Background(), deadline)
}

func await(caller context.Context, callerDone <-chan struct{}, clean context.Context, done <-chan error) error {
	select {
	case err := <-done:
		return err
	case <-callerDone:
		return canceledContextError(caller)
	case <-clean.Done():
		return clean.Err()
	}
}

func (p *Pipeline) run() {
	defer close(p.done)
	defer p.cancelWork()
	for {
		select {
		case request := <-p.closeRequest:
			p.shutdown(request.ctx)
			return
		default:
		}
		select {
		case request := <-p.closeRequest:
			p.shutdown(request.ctx)
			return
		case item := <-p.queue:
			p.consume(item)
		}
	}
}

func (p *Pipeline) consume(item queueItem) {
	if item.flush != nil {
		err := item.flush.ctx.Err()
		if err == nil {
			err = p.workContext.Err()
		}
		if err == nil {
			ctx, cancel := context.WithCancel(item.flush.ctx)
			stop := context.AfterFunc(p.workContext, cancel)
			err = p.invoke(ctx, p.exporter.Flush)
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			stop()
			cancel()
		}
		item.flush.done <- err
		return
	}
	if p.workContext.Err() != nil {
		p.dropped.Add(1)
		return
	}
	ctx, cancel := context.WithTimeout(p.workContext, p.exportTimeout)
	err := p.invoke(ctx, func(ctx context.Context) error { return p.exporter.Export(ctx, item.event) })
	cancel()
	if err != nil {
		p.dropped.Add(1)
	} else {
		p.exported.Add(1)
	}
}

func (p *Pipeline) shutdown(ctx context.Context) {
	if ctx.Err() != nil {
		p.cancelWork()
	}
	for {
		select {
		case item := <-p.queue:
			p.consume(item)
		default:
			flushErr := p.invoke(ctx, p.exporter.Flush)
			closeErr := p.invoke(ctx, p.exporter.Close)
			if flushErr != nil || closeErr != nil {
				p.closeErr = ErrExporter
			}
			if ctx.Err() != nil {
				p.closeErr = ctx.Err()
			}
			return
		}
	}
}

func (p *Pipeline) invoke(ctx context.Context, callback func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrExporter
		}
		if err != nil {
			p.exportFailures.Add(1)
			err = ErrExporter
		}
	}()
	return callback(ctx)
}
