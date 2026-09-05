// Package signals implements synchronous process-local typed event dispatch.
package signals

import (
	"context"
	"errors"
	"fmt"
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
func (s *Signal[T]) send(ctx context.Context, value T, robust bool) ([]Result, error) {
	s.mu.RLock()
	list := slices.Clone(s.receivers)
	s.mu.RUnlock()
	results := make([]Result, 0, len(list))
	var errs []error
	for _, r := range list {
		if err := ctx.Err(); err != nil {
			return results, errors.Join(append(errs, err)...)
		}
		err := invoke(ctx, r.receiver, value)
		results = append(results, Result{r.id, err})
		if err != nil {
			errs = append(errs, fmt.Errorf("receiver %s: %w", r.id, err))
			if !robust {
				break
			}
		}
	}
	return results, errors.Join(errs...)
}
func (s *Signal[T]) Send(ctx context.Context, value T) ([]Result, error) {
	return s.send(ctx, value, false)
}
func (s *Signal[T]) SendRobust(ctx context.Context, value T) ([]Result, error) {
	return s.send(ctx, value, true)
}
