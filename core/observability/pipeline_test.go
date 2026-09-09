package observability

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testExporter struct {
	mu              sync.Mutex
	events          []Event
	export          func(context.Context, Event) error
	flush           func(context.Context) error
	close           func(context.Context) error
	flushes, closes atomic.Int32
}

func (e *testExporter) Export(ctx context.Context, event Event) error {
	if e.export != nil {
		return e.export(ctx, event)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return nil
}
func (e *testExporter) Flush(ctx context.Context) error {
	e.flushes.Add(1)
	if e.flush != nil {
		return e.flush(ctx)
	}
	return nil
}
func (e *testExporter) Close(ctx context.Context) error {
	e.closes.Add(1)
	if e.close != nil {
		return e.close(ctx)
	}
	return nil
}
func (e *testExporter) recorded() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.events...)
}

func newTestPipeline(t *testing.T, exporter Exporter, mutate func(*Config)) *Pipeline {
	t.Helper()
	config := Config{Operations: []string{"http.request", "db.select"}, Routes: []string{"/books/<int:id>/"}, SampleRate: SampleAll, ShutdownTimeout: time.Second}
	if mutate != nil {
		mutate(&config)
	}
	p, err := New(config, exporter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

func startTestSpan(t *testing.T, p *Pipeline) *Span {
	t.Helper()
	_, span, err := p.Start(context.Background(), "http.request", "/books/<int:id>/")
	if err != nil {
		t.Fatal(err)
	}
	return span
}

func waitTelemetry(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("telemetry condition did not complete")
		}
		runtime.Gosched()
	}
}

func receiveTelemetry[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case value := <-channel:
		return value
	case <-timer.C:
		t.Fatal("telemetry channel did not complete")
		var zero T
		return zero
	}
}

func TestConfigBoundsAndSnapshot(t *testing.T) {
	invalid := []Config{
		{}, {Operations: []string{"a", "a"}}, {Operations: []string{"a\nvalue"}},
		{Operations: []string{strings.Repeat("a", maxOperationBytes+1)}},
		{Operations: make([]string, maxOperations+1)},
		{Operations: []string{"a"}, Routes: make([]string, maxRoutes+1)},
		{Operations: []string{"a"}, Routes: []string{"/books/?token=value"}},
		{Operations: []string{"a"}, Routes: []string{"/a", "/a"}},
		{Operations: []string{"a"}, Routes: []string{"not-a-route"}},
		{Operations: []string{"a"}, Routes: []string{"/" + strings.Repeat("a", maxRouteBytes)}},
		{Operations: []string{"a"}, QueueSize: -1},
		{Operations: []string{"a"}, QueueSize: MaxQueueSize + 1},
		{Operations: []string{"a"}, SampleRate: SampleAll + 1},
		{Operations: []string{"a"}, ExportTimeout: -1},
		{Operations: []string{"a"}, ExportTimeout: time.Minute + 1},
		{Operations: []string{"a"}, ShutdownTimeout: -1},
		{Operations: []string{"a"}, ShutdownTimeout: time.Minute + 1},
	}
	for i, config := range invalid {
		if p, err := New(config, &testExporter{}); p != nil || err != ErrInvalidConfig {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	var typedNil *testExporter
	if _, err := New(Config{Operations: []string{"a"}}, typedNil); err != ErrInvalidConfig {
		t.Fatal(err)
	}
	exporter := &testExporter{}
	config := Config{Operations: []string{"http.request"}, Routes: []string{"/books/<int:id>/"}, SampleRate: SampleAll}
	p, err := New(config, exporter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	config.Operations[0], config.Routes[0] = "modified", "/modified"
	if !startTestSpan(t, p).End(StatusSuccess) {
		t.Fatal("registered snapshot was lost")
	}
	if _, _, err := p.Start(context.Background(), "modified", ""); err != ErrInvalidEvent {
		t.Fatal(err)
	}
	if _, _, err := p.Start(context.Background(), "http.request", "/books/123/"); err != ErrInvalidEvent {
		t.Fatal("runtime URL was accepted", err)
	}
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := exporter.recorded(); len(got) != 1 || got[0].Operation() != "http.request" || got[0].Route() != "/books/<int:id>/" {
		t.Fatal(got)
	}
}

func TestExplicitCorrelationAndOneShotConcurrentEnd(t *testing.T) {
	exporter := &testExporter{}
	p := newTestPipeline(t, exporter, nil)
	ids := Correlation{TraceID: ID{1}, RequestID: ID{2}, TaskID: ID{3}, SpanID: SpanID{4}}
	ctx, err := WithCorrelation(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	childCtx, span, err := p.Start(ctx, "db.select", "")
	if err != nil {
		t.Fatal(err)
	}
	child, ok := CorrelationFromContext(childCtx)
	if !ok || child.TraceID != ids.TraceID || child.RequestID != ids.RequestID || child.TaskID != ids.TaskID || child.SpanID == ids.SpanID || child.SpanID == (SpanID{}) {
		t.Fatal(child)
	}
	var ended atomic.Int32
	var group sync.WaitGroup
	for range 128 {
		group.Go(func() {
			if span.End(StatusSuccess) {
				ended.Add(1)
			}
		})
	}
	group.Wait()
	if ended.Load() != 1 {
		t.Fatal(ended.Load())
	}
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := exporter.recorded()
	if len(events) != 1 || events[0].ParentSpanID() != ids.SpanID || events[0].Correlation() != child || events[0].Duration() < 0 || events[0].Duration() > MaxDuration {
		t.Fatal(events)
	}
	if p.Stats().Exported != 1 || p.Stats().Dropped != 0 {
		t.Fatal(p.Stats())
	}
	rootCtx, _ := WithCorrelation(context.Background(), Correlation{SpanID: SpanID{9}})
	_, root, err := p.Start(rootCtx, "db.select", "")
	if err != nil || root.event.parent != (SpanID{}) {
		t.Fatal("new trace retained foreign parent", err)
	}
}

func TestSamplingAndDurationBounds(t *testing.T) {
	exporter := &testExporter{}
	p := newTestPipeline(t, exporter, func(config *Config) { config.SampleRate = 0 })
	if startTestSpan(t, p).End(StatusSuccess) {
		t.Fatal("zero sampling emitted")
	}
	if p.Stats().SampledOut != 1 || p.Stats().Dropped != 0 {
		t.Fatal(p.Stats())
	}
	full := newTestPipeline(t, exporter, nil)
	span := startTestSpan(t, full)
	span.started = time.Now().Add(-2 * MaxDuration)
	span.End(StatusFailure)
	future := startTestSpan(t, full)
	future.started = time.Now().Add(time.Hour)
	future.End(StatusSuccess)
	if err := full.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := exporter.recorded()
	if len(got) != 2 || got[0].Duration() != MaxDuration || got[1].Duration() != 0 {
		t.Fatal(got)
	}
	invalid := startTestSpan(t, full)
	if invalid.End(Status(255)) || invalid.End(StatusSuccess) || full.Stats().Dropped != 1 {
		t.Fatal(full.Stats())
	}
	for code, want := range map[int]Status{99: StatusUnknown, 100: StatusHTTP1xx, 200: StatusHTTP2xx, 399: StatusHTTP3xx, 400: StatusHTTP4xx, 599: StatusHTTP5xx, 600: StatusUnknown} {
		if got := HTTPStatus(code); got != want {
			t.Fatalf("%d: %v", code, got)
		}
	}
}

func TestQueueOverflowAndStalledExporterCloseAreBounded(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	exporter := &testExporter{export: func(context.Context, Event) error { close(entered); <-release; return nil }}
	p := newTestPipeline(t, exporter, func(config *Config) { config.QueueSize = 1 })
	if !startTestSpan(t, p).End(StatusSuccess) {
		t.Fatal("first rejected")
	}
	receiveTelemetry(t, entered)
	if !startTestSpan(t, p).End(StatusSuccess) {
		t.Fatal("buffered rejected")
	}
	if startTestSpan(t, p).End(StatusSuccess) {
		t.Fatal("overflow admitted")
	}
	if err := p.Flush(context.Background()); err != ErrBusy {
		t.Fatal(err)
	}
	late := startTestSpan(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Close(ctx); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if late.End(StatusSuccess) {
		t.Fatal("closed admission accepted")
	}
	if _, _, err := p.Start(context.Background(), "db.select", ""); err != ErrClosed {
		t.Fatal(err)
	}
	if exporter.closes.Load() != 0 {
		t.Fatal("concurrent provider cleanup")
	}
	releaseOnce.Do(func() { close(release) })
	waitTelemetry(t, func() bool {
		select {
		case <-p.done:
			return true
		default:
			return false
		}
	})
	if exporter.closes.Load() != 1 || exporter.flushes.Load() != 1 {
		t.Fatal("cleanup was not attempted exactly once")
	}
	if stats := p.Stats(); stats.Exported != 1 || stats.Dropped != 3 {
		t.Fatal(stats)
	}
}

func TestExporterErrorsAndPanicsStayLocalAndWorkerRecovers(t *testing.T) {
	var calls atomic.Int32
	exporter := &testExporter{export: func(context.Context, Event) error {
		switch calls.Add(1) {
		case 1:
			return errors.New("private-provider-marker")
		case 2:
			panic("private-panic-marker")
		}
		return nil
	}}
	p := newTestPipeline(t, exporter, nil)
	for range 3 {
		if !startTestSpan(t, p).End(StatusSuccess) {
			t.Fatal("unexpected admission failure")
		}
	}
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := p.Stats(); stats.Exported != 1 || stats.Dropped != 2 || stats.ExportFailures != 2 {
		t.Fatal(stats)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(context.Background()); err != nil || exporter.closes.Load() != 1 {
		t.Fatal(err)
	}
}

func TestFlushCancellationAndShutdownSkipQueuedBarriers(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	exporter := &testExporter{flush: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	p := newTestPipeline(t, exporter, func(config *Config) { config.QueueSize = 4 })
	first := make(chan error, 1)
	go func() { first <- p.Flush(context.Background()) }()
	receiveTelemetry(t, entered)
	second := make(chan error, 1)
	go func() { second <- p.Flush(context.Background()) }()
	waitTelemetry(t, func() bool { return len(p.queue) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Close(ctx); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if err := receiveTelemetry(t, first); err != context.Canceled {
		t.Fatal(err)
	}
	if err := receiveTelemetry(t, second); err != context.Canceled {
		t.Fatal(err)
	}
	waitTelemetry(t, func() bool {
		select {
		case <-p.done:
			return true
		default:
			return false
		}
	})
	if calls.Load() != 2 || exporter.closes.Load() != 1 {
		t.Fatalf("active barrier + final flush only: %d", calls.Load())
	}
}

func TestSamplingIsStableWithinTrace(t *testing.T) {
	exporter := &testExporter{}
	p := newTestPipeline(t, exporter, func(config *Config) { config.SampleRate = SampleAll / 2 })
	for _, value := range []uint64{0, uint64(SampleAll / 2)} {
		var id ID
		id[15] = 1
		binary.BigEndian.PutUint64(id[:8], value)
		ctx, err := WithCorrelation(context.Background(), Correlation{TraceID: id})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			child, span, err := p.Start(ctx, "db.select", "")
			if err != nil {
				t.Fatal(err)
			}
			if admitted := span.End(StatusSuccess); admitted != (value == 0) {
				t.Fatal("trace sample changed")
			}
			ctx = child
		}
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := p.Stats(); stats.Exported != 2 || stats.SampledOut != 2 || stats.Dropped != 0 {
		t.Fatal(stats)
	}
}

func TestFlushDeadlineAndCleanupFailureAreStable(t *testing.T) {
	var calls atomic.Int32
	exporter := &testExporter{
		flush: func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		close: func(context.Context) error { panic("private-cleanup-marker") },
	}
	p := newTestPipeline(t, exporter, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Flush(ctx); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if err := p.Close(context.Background()); err != ErrExporter {
		t.Fatal(err)
	}
	if exporter.closes.Load() != 1 {
		t.Fatal("cleanup not attempted")
	}
}

func TestAlreadyCanceledCloseStillAttemptsOwnedCleanup(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		exporter := &testExporter{}
		p := newTestPipeline(t, exporter, nil)
		var ctx context.Context
		var cancel context.CancelFunc
		want := context.Canceled
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		if err := p.Close(ctx); err != want {
			t.Fatal(err)
		}
		cancel()
		waitTelemetry(t, func() bool {
			select {
			case <-p.done:
				return true
			default:
				return false
			}
		})
		if exporter.flushes.Load() != 1 || exporter.closes.Load() != 1 {
			t.Fatal("canceled owner lost cleanup")
		}
	}
}

func TestConcurrentFlushAndCloseHaveBoundedOwnership(t *testing.T) {
	exporter := &testExporter{}
	p := newTestPipeline(t, exporter, func(config *Config) { config.QueueSize = 8 })
	spans := make([]*Span, 128)
	for i := range spans {
		spans[i] = startTestSpan(t, p)
	}
	var group sync.WaitGroup
	for _, span := range spans {
		group.Go(func() { span.End(StatusSuccess) })
	}
	for range 32 {
		group.Go(func() {
			err := p.Flush(context.Background())
			if err != nil && err != ErrBusy && err != ErrClosed {
				t.Error(err)
			}
		})
	}
	for range 8 {
		group.Go(func() {
			if err := p.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	stats := p.Stats()
	if stats.Exported+stats.Dropped != uint64(len(spans)) || exporter.closes.Load() != 1 {
		t.Fatal(stats, exporter.closes.Load())
	}
}
