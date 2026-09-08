// Package signals implements synchronous process-local typed event dispatch.
package signals

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
)

type Receiver[T any] func(context.Context, T) error
type registration[T any] struct {
	id       string
	priority int
	receiver Receiver[T]
}
type Result struct {
	ReceiverID string
	Err        error
}
type Signal[T any] struct {
	mu        sync.RWMutex
	receivers []registration[T]
}

func (s *Signal[T]) Connect(id string, priority int, receiver Receiver[T]) error {
	if id == "" || receiver == nil {
		return errors.New("signal receiver ID and handler required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.receivers {
		if r.id == id {
			return errors.New("duplicate signal receiver")
		}
	}
	s.receivers = append(s.receivers, registration[T]{id, priority, receiver})
	slices.SortStableFunc(s.receivers, func(a, b registration[T]) int {
		if a.priority < b.priority {
			return -1
		}
		if a.priority > b.priority {
			return 1
		}
		return 0
	})
	return nil
}
func (s *Signal[T]) Disconnect(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.receivers {
		if r.id == id {
			s.receivers = slices.Delete(s.receivers, i, i+1)
			return true
		}
	}
	return false
}
func invoke[T any](ctx context.Context, receiver Receiver[T], value T) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("signal receiver panicked")
		}
	}()
	return receiver(ctx, value)
}

var errInvalidContext = errors.New("invalid signal context")

// Context is a caller-supplied interface. Check nil implementations without
// calling their methods, and do not expose values from a panicking Err method.
func signalContextError(ctx context.Context) (err error) {
	err = errInvalidContext
	defer func() {
		if recover() != nil {
			err = errInvalidContext
		}
	}()
	if ctx == nil {
		return errInvalidContext
	}
	value := reflect.ValueOf(ctx)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return errInvalidContext
		}
	}
	return ctx.Err()
}

func (s *Signal[T]) send(ctx context.Context, value T, robust bool) ([]Result, error) {
	// Freeze dispatch ownership before invoking even a caller-defined Context method.
	s.mu.RLock()
	list := slices.Clone(s.receivers)
	s.mu.RUnlock()
	results := make([]Result, 0, len(list))
	var errs []error
	if err := signalContextError(ctx); err != nil {
		return results, err
	}
	for _, r := range list {
		err := invoke(ctx, r.receiver, value)
		results = append(results, Result{r.id, err})
		if err != nil {
			errs = append(errs, fmt.Errorf("receiver %s: %w", r.id, err))
		}
		// This is both the pre-next-receiver and final dispatch boundary. Keep
		// completed results intact: cancellation does not undo receiver effects.
		if ctxErr := signalContextError(ctx); ctxErr != nil {
			errs = append(errs, ctxErr)
			break
		}
		if err != nil && !robust {
			break
		}
	}
	return results, errors.Join(errs...)
}

// Send invokes a frozen receiver snapshot synchronously in priority order and
// stops on the first receiver error or context failure. Returned results describe
// only invoked receivers; the dispatch error also includes observed cancellation.
func (s *Signal[T]) Send(ctx context.Context, value T) ([]Result, error) {
	return s.send(ctx, value, false)
}

// SendRobust continues after receiver errors and panics, but not context failure.
// It preserves every completed result and joins receiver errors with any observed
// context failure. Neither dispatch mode rolls back receiver effects.
func (s *Signal[T]) SendRobust(ctx context.Context, value T) ([]Result, error) {
	return s.send(ctx, value, true)
}
