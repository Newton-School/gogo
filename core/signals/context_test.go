package signals

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func signalModes(t *testing.T, test func(*testing.T, bool)) {
	t.Helper()
	for _, robust := range []bool{false, true} {
		name := "ordinary"
		if robust {
			name = "robust"
		}
		t.Run(name, func(t *testing.T) { test(t, robust) })
	}
}

func TestDispatchCanceledEntryAndEmpty(t *testing.T) {
	signalModes(t, func(t *testing.T, robust bool) {
		for _, populated := range []bool{false, true} {
			t.Run(fmt.Sprint(populated), func(t *testing.T) {
				var signal Signal[int]
				if populated {
					_ = signal.Connect("unused", 0, func(context.Context, int) error {
						t.Fatal("receiver ran after entry cancellation")
						return nil
					})
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				results, err := signal.send(ctx, 1, robust)
				if len(results) != 0 || !errors.Is(err, context.Canceled) {
					t.Fatalf("results=%v error=%v", results, err)
				}
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
				defer cancel()
				results, err = signal.send(ctx, 1, robust)
				if len(results) != 0 || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline results=%v error=%v", results, err)
				}
			})
		}
	})
}

func TestDispatchFinalCancellationPreservesCompletedResult(t *testing.T) {
	signalModes(t, func(t *testing.T, robust bool) {
		for _, receiverErr := range []error{nil, errors.New("receiver failed")} {
			t.Run(fmt.Sprint(receiverErr != nil), func(t *testing.T) {
				var signal Signal[int]
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				effects := 0
				_ = signal.Connect("finished", 0, func(received context.Context, value int) error {
					if received != ctx || value != 7 {
						t.Fatal("dispatch changed its context or payload")
					}
					effects++
					cancel()
					return receiverErr
				})
				results, err := signal.send(ctx, 7, robust)
				if effects != 1 || len(results) != 1 || results[0].ReceiverID != "finished" || results[0].Err != receiverErr {
					t.Fatalf("effects=%d results=%v", effects, results)
				}
				if !errors.Is(err, context.Canceled) || receiverErr != nil && !errors.Is(err, receiverErr) {
					t.Fatalf("lost cancellation or receiver error: %v", err)
				}
			})
		}
	})
}

func TestDispatchCancellationKeepsEveryPriorError(t *testing.T) {
	signalModes(t, func(t *testing.T, robust bool) {
		var signal Signal[int]
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first, last := errors.New("first failure"), errors.New("last failure")
		_ = signal.Connect("first", 0, func(context.Context, int) error { return first })
		_ = signal.Connect("panic", 1, func(context.Context, int) error { panic("private receiver payload") })
		_ = signal.Connect("cancel", 2, func(context.Context, int) error { cancel(); return last })
		_ = signal.Connect("unused", 3, func(context.Context, int) error {
			t.Fatal("receiver ran after ordinary failure or robust cancellation")
			return nil
		})
		results, err := signal.send(ctx, 0, robust)
		if len(results) == 0 || !errors.Is(err, first) || results[0].Err != first {
			t.Fatalf("lost first error: results=%v error=%v", results, err)
		}
		if !robust {
			if len(results) != 1 || errors.Is(err, context.Canceled) {
				t.Fatalf("ordinary send continued: results=%v error=%v", results, err)
			}
			return
		}
		if len(results) != 3 || results[1].ReceiverID != "panic" || results[1].Err == nil || results[2].Err != last {
			t.Fatalf("lost ordered results: %v", results)
		}
		if !errors.Is(err, last) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "signal receiver panicked") || strings.Contains(err.Error(), "private receiver payload") {
			t.Fatalf("incorrect collected errors: %v", err)
		}
	})
}

type dispatchContext struct {
	context.Context
	onErr func() error
}

func (ctx *dispatchContext) Err() error {
	// A typed-nil context must be rejected even when its Err method tolerates nil.
	if ctx == nil {
		return nil
	}
	return ctx.onErr()
}

type nilMapContext map[string]string

func (nilMapContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (nilMapContext) Done() <-chan struct{}       { return nil }
func (nilMapContext) Err() error                  { return nil }
func (nilMapContext) Value(any) any               { return nil }

func TestDispatchInvalidContextFailsSafely(t *testing.T) {
	signalModes(t, func(t *testing.T, robust bool) {
		contexts := map[string]context.Context{
			"nil":           nil,
			"typed pointer": (*dispatchContext)(nil),
			"typed map":     nilMapContext(nil),
			"panic": &dispatchContext{Context: context.Background(), onErr: func() error {
				panic("private context payload")
			}},
		}
		for name, ctx := range contexts {
			t.Run(name, func(t *testing.T) {
				for _, populated := range []bool{false, true} {
					var signal Signal[int]
					if populated {
						_ = signal.Connect("unused", 0, func(context.Context, int) error {
							t.Fatal("receiver ran with invalid context")
							return nil
						})
					}
					results, err := signal.send(ctx, 0, robust)
					if len(results) != 0 || err == nil || err.Error() != "invalid signal context" {
						t.Fatalf("results=%v error=%v", results, err)
					}
				}
			})
		}
	})
}

func TestDispatchFinalContextPanicKeepsReceiverError(t *testing.T) {
	signalModes(t, func(t *testing.T, robust bool) {
		var signal Signal[int]
		calls := 0
		ctx := &dispatchContext{Context: context.Background(), onErr: func() error {
			calls++
			if calls > 1 {
				panic("private context payload")
			}
			return nil
		}}
		receiverErr := errors.New("receiver failure")
		_ = signal.Connect("completed", 0, func(context.Context, int) error { return receiverErr })
		results, err := signal.send(ctx, 0, robust)
		if calls != 2 || len(results) != 1 || results[0].Err != receiverErr || !errors.Is(err, receiverErr) || !strings.Contains(err.Error(), "invalid signal context") || strings.Contains(err.Error(), "private context payload") {
			t.Fatalf("calls=%d results=%v error=%v", calls, results, err)
		}
	})
}

func TestDispatchSnapshotsBeforeContextCallback(t *testing.T) {
	signalModes(t, func(t *testing.T, robust bool) {
		var signal Signal[int]
		for _, id := range []string{"first", "second"} {
			if err := signal.Connect(id, 1, func(context.Context, int) error { return nil }); err != nil {
				t.Fatal(err)
			}
		}
		calls := 0
		ctx := &dispatchContext{Context: context.Background(), onErr: func() error {
			calls++
			if calls == 1 {
				if !signal.Disconnect("second") {
					t.Fatal("missing initial registration")
				}
				if err := signal.Connect("new", 0, func(context.Context, int) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		}}
		results, err := signal.send(ctx, 0, robust)
		if err != nil || !reflect.DeepEqual(results, []Result{{ReceiverID: "first"}, {ReceiverID: "second"}}) || calls != 3 {
			t.Fatalf("first snapshot=%v error=%v context calls=%d", results, err, calls)
		}
		results, err = signal.send(context.Background(), 0, robust)
		if err != nil || !reflect.DeepEqual(results, []Result{{ReceiverID: "new"}, {ReceiverID: "first"}}) {
			t.Fatalf("next snapshot=%v error=%v", results, err)
		}
	})
}

func TestDispatchConcurrentRegistrationOwnsSnapshot(t *testing.T) {
	signalModes(t, func(t *testing.T, robust bool) {
		var signal Signal[int]
		entered, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		var oldCalls, newCalls atomic.Int32
		_ = signal.Connect("first", 0, func(_ context.Context, value int) error {
			if value == 1 {
				close(entered)
				<-release
			}
			return nil
		})
		_ = signal.Connect("later", 1, func(context.Context, int) error { oldCalls.Add(1); return nil })
		type outcome struct {
			results []Result
			err     error
		}
		finished := make(chan outcome, 1)
		go func() {
			results, err := signal.send(context.Background(), 1, robust)
			finished <- outcome{results, err}
		}()
		<-entered
		if !signal.Disconnect("later") {
			t.Fatal("missing registration")
		}
		if err := signal.Connect("later", -1, func(context.Context, int) error { newCalls.Add(1); return nil }); err != nil {
			t.Fatal(err)
		}
		if err := signal.Connect("later", 10, func(context.Context, int) error { return nil }); err == nil {
			t.Fatal("duplicate ID accepted")
		}
		next, err := signal.send(context.Background(), 2, robust)
		if err != nil || !reflect.DeepEqual(next, []Result{{ReceiverID: "later"}, {ReceiverID: "first"}}) {
			t.Fatalf("next snapshot=%v error=%v", next, err)
		}
		releaseOnce.Do(func() { close(release) })
		previous := <-finished
		if previous.err != nil || !reflect.DeepEqual(previous.results, []Result{{ReceiverID: "first"}, {ReceiverID: "later"}}) || oldCalls.Load() != 1 || newCalls.Load() != 1 {
			t.Fatalf("previous=%v old=%d new=%d", previous, oldCalls.Load(), newCalls.Load())
		}
		// Results belong to the caller; changing a completed result cannot rename a registration.
		previous.results[0].ReceiverID = "changed"
		last, err := signal.send(context.Background(), 3, robust)
		if err != nil || !reflect.DeepEqual(last, next) {
			t.Fatalf("mutated result changed registrations: %v error=%v", last, err)
		}
	})
}
