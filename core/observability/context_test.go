package observability

import (
	"context"
	"errors"
	"testing"
	"time"
)

type unusualContext struct {
	context.Context
	onErr      func() error
	onDone     func() <-chan struct{}
	onDeadline func() (time.Time, bool)
	onValue    func(any) any
}

func (c *unusualContext) Err() error {
	if c.onErr != nil {
		return c.onErr()
	}
	return c.Context.Err()
}
func (c *unusualContext) Done() <-chan struct{} {
	if c.onDone != nil {
		return c.onDone()
	}
	return c.Context.Done()
}
func (c *unusualContext) Deadline() (time.Time, bool) {
	if c.onDeadline != nil {
		return c.onDeadline()
	}
	return c.Context.Deadline()
}
func (c *unusualContext) Value(key any) any {
	if c.onValue != nil {
		return c.onValue(key)
	}
	return c.Context.Value(key)
}

func TestMalformedContextsAreRefusedWithoutProviderData(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	var typedNil *unusualContext
	cases := []context.Context{
		nil, typedNil,
		&unusualContext{Context: context.Background(), onErr: func() error { panic("private-context-marker") }},
		&unusualContext{Context: context.Background(), onErr: func() error { return errors.New("private-context-marker") }},
		&unusualContext{Context: context.Background(), onDone: func() <-chan struct{} { return closed }},
		&unusualContext{Context: context.Background(), onDeadline: func() (time.Time, bool) { panic("private-context-marker") }},
		&unusualContext{Context: context.Background(), onDone: func() <-chan struct{} { panic("private-context-marker") }},
	}
	p := newTestPipeline(t, &testExporter{}, nil)
	for i, ctx := range cases {
		if _, _, err := p.Start(ctx, "db.select", ""); err != ErrInvalidContext {
			t.Fatalf("Start %d: %v", i, err)
		}
		if _, err := WithCorrelation(ctx, Correlation{}); err != ErrInvalidContext {
			t.Fatalf("WithCorrelation %d: %v", i, err)
		}
		if _, ok := CorrelationFromContext(ctx); ok {
			t.Fatalf("Correlation %d accepted", i)
		}
		if err := p.Flush(ctx); err != ErrInvalidContext {
			t.Fatalf("Flush %d: %v", i, err)
		}
		if err := p.Close(ctx); err != ErrInvalidContext {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
	panicValue := &unusualContext{Context: context.Background(), onValue: func(any) any { panic("private-context-marker") }}
	if _, _, err := p.Start(panicValue, "db.select", ""); err != ErrInvalidContext {
		t.Fatal(err)
	}
	if _, ok := CorrelationFromContext(panicValue); ok {
		t.Fatal("panicking Value accepted")
	}
}

func TestContextCancellationInsideCallbacksIsObserved(t *testing.T) {
	for _, method := range []string{"deadline", "done", "value"} {
		t.Run(method, func(t *testing.T) {
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &unusualContext{Context: base}
			switch method {
			case "deadline":
				ctx.onDeadline = func() (time.Time, bool) { cancel(); return time.Time{}, false }
			case "done":
				ctx.onDone = func() <-chan struct{} { cancel(); return base.Done() }
			case "value":
				ctx.onValue = func(any) any { cancel(); return Correlation{TraceID: ID{1}} }
			}
			p := newTestPipeline(t, &testExporter{}, nil)
			if _, _, err := p.Start(ctx, "db.select", ""); err != context.Canceled {
				t.Fatal(err)
			}
			if _, ok := CorrelationFromContext(ctx); ok {
				t.Fatal("canceled correlation accepted")
			}
		})
	}
}

func TestExportContextsNeverCarryCallerValues(t *testing.T) {
	type privateKey struct{}
	exporter := &testExporter{
		export: func(ctx context.Context, _ Event) error {
			if ctx.Value(privateKey{}) != nil {
				t.Error("request data reached exporter")
			}
			return nil
		},
		flush: func(ctx context.Context) error {
			if ctx.Value(privateKey{}) != nil {
				t.Error("flush data reached exporter")
			}
			return nil
		},
		close: func(ctx context.Context) error {
			if ctx.Value(privateKey{}) != nil {
				t.Error("close data reached exporter")
			}
			return nil
		},
	}
	p := newTestPipeline(t, exporter, nil)
	ctx := context.WithValue(context.Background(), privateKey{}, "private-context-marker")
	_, span, err := p.Start(ctx, "db.select", "")
	if err != nil {
		t.Fatal(err)
	}
	span.End(StatusSuccess)
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
