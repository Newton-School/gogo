package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

type coreDispatchContext struct {
	context.Context
	onErr func() error
}

func (c *coreDispatchContext) Err() error { return c.onErr() }

func coreDispatchTerminal(record async.Record) async.Record {
	record.State, record.Output = async.Succeeded, json.RawMessage("1")
	record.FinishedAt = time.Now().UTC()
	record.PayloadExpiresAt = record.FinishedAt.Add(time.Hour)
	record.TombstoneUntil = record.FinishedAt.Add(24 * time.Hour)
	return record
}

// These regressions exercise the existing Result boundary directly, so the
// bridge cannot hide a context/ownership defect with an outer recovery wrapper.
func TestCoreDispatchResultDependencyFreezesBeforeContext(t *testing.T) {
	for _, method := range []string{"snapshot", "get", "forget", "revoke"} {
		t.Run(method, func(t *testing.T) {
			result, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
			other, otherStore, _ := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
			original := result.Receipt.ID
			reads, writes, wrong := 0, 0, 0
			record = coreDispatchTerminal(record)
			store.lookup = func(_ context.Context, id string) (async.Record, error) {
				reads++
				if id != original {
					t.Fatal("retargeted identity")
				}
				return record, nil
			}
			store.forget = func(_ context.Context, id string) error {
				writes++
				if id != original {
					t.Fatal("wrong forget")
				}
				return nil
			}
			store.revoke = func(_ context.Context, id, _ string) error {
				writes++
				if id != original {
					t.Fatal("wrong revoke")
				}
				return nil
			}
			otherStore.lookup = func(context.Context, string) (async.Record, error) {
				wrong++
				return async.Record{}, async.ErrUnavailable
			}
			armed := true
			ctx := &coreDispatchContext{Context: context.Background(), onErr: func() error {
				if armed {
					armed = false
					*result = *other
				}
				return nil
			}}
			var err error
			switch method {
			case "snapshot":
				_, err = result.Snapshot(ctx)
			case "get":
				_, err = result.Get(ctx)
			case "forget":
				err = result.Forget(ctx)
			case "revoke":
				err = result.Revoke(ctx)
			}
			if err != nil || reads != 1 || wrong != 0 || writes != boolInt(method == "forget" || method == "revoke") {
				t.Fatal("context replaced the in-flight result", err, reads, wrong, writes)
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestCoreDispatchResultDependencyDetachesBeforeContext(t *testing.T) {
	for _, method := range []string{"snapshot", "get"} {
		t.Run(method, func(t *testing.T) {
			result, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
			record = coreDispatchTerminal(record)
			record.Envelope.Headers = map[string]string{"label": "original"}
			record.Digest = record.Envelope.Digest()
			armed := false
			store.lookup = func(context.Context, string) (async.Record, error) { armed = true; return record, nil }
			ctx := &coreDispatchContext{Context: context.Background(), onErr: func() error {
				if armed {
					armed = false
					record.Output[0] = '9'
					record.Envelope.Headers["label"] = "changed"
				}
				return nil
			}}
			if method == "get" {
				if value, err := result.Get(ctx); err != nil || value != 1 {
					t.Fatal("context rewrote provider output", value, err)
				}
			} else if value, err := result.Snapshot(ctx); err != nil || string(value.Output) != "1" || value.Envelope.Headers["label"] != "original" {
				t.Fatal("context rewrote provider metadata", err)
			}
		})
	}
}

func TestCoreDispatchResultDependencySafeContexts(t *testing.T) {
	var typedNil *coreDispatchContext
	for name, ctx := range map[string]context.Context{
		"nil": nil, "typed nil": typedNil,
		"panic":         &coreDispatchContext{Context: context.Background(), onErr: func() error { panic("private context") }},
		"invalid error": &coreDispatchContext{Context: context.Background(), onErr: func() error { return errors.New("private context") }},
	} {
		for _, method := range []string{"snapshot", "get", "forget", "revoke"} {
			t.Run(name+"/"+method, func(t *testing.T) {
				defer func() {
					if recover() != nil {
						t.Error("context panic escaped the Result boundary")
					}
				}()
				result, store, _ := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
				store.lookup = func(context.Context, string) (async.Record, error) {
					t.Error("invalid context reached provider")
					return async.Record{}, async.ErrUnavailable
				}
				var err error
				switch method {
				case "snapshot":
					_, err = result.Snapshot(ctx)
				case "get":
					_, err = result.Get(ctx)
				case "forget":
					err = result.Forget(ctx)
				case "revoke":
					err = result.Revoke(ctx)
				}
				want := async.ErrUnavailable
				if name == "nil" || name == "typed nil" {
					want = async.ErrInvalid
				}
				if err != want {
					t.Fatal("unsafe context outcome", err)
				}
			})
		}
	}
}

type resultClosedDoneContext struct {
	context.Context
	done <-chan struct{}
}

func (c resultClosedDoneContext) Done() <-chan struct{} { return c.done }

func TestResultClosedDoneWithoutErrorNeverCompletesQueuedTask(t *testing.T) {
	for _, method := range []string{"get", "wait"} {
		t.Run(method, func(t *testing.T) {
			result, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
			reads := 0
			store.lookup = func(context.Context, string) (async.Record, error) { reads++; return record, nil }
			done := make(chan struct{})
			close(done)
			ctx := resultClosedDoneContext{Context: context.Background(), done: done}
			wait := result.Get
			if method == "wait" {
				wait = result.Wait
			}
			if value, err := wait(ctx); value != 0 || err != async.ErrUnavailable || reads != 1 {
				t.Fatal("malformed context fabricated queued task success", value, err, reads)
			}
		})
	}
}
