package backends

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Newton-School/gogo/queue"
)

var (
	ErrResultNotFound = errors.New("result not found")
	ErrResultExpired  = errors.New("result expired")
)

// Backend is the queue result backend contract.
type Backend interface {
	StoreResult(context.Context, queue.Result) error
	GetResult(context.Context, string) (queue.Result, error)
	Forget(context.Context, string) error
	Wait(context.Context, string, time.Duration) (queue.Result, error)
	Children(context.Context, string) ([]string, error)
	GroupResult(context.Context, string, []string) (queue.GroupResult, error)
	ChordCounter(context.Context, string, int) (int, error)
}

type MemoryOptions struct {
	Now func() time.Time
}

func init() {
	queue.RegisterResultBackendFactory("memory", func(queue.RuntimeConfig) (queue.ResultBackend, error) {
		return NewMemoryBackend(MemoryOptions{}), nil
	})
}

// MemoryBackend stores result data in process memory.
type MemoryBackend struct {
	mu       sync.RWMutex
	now      func() time.Time
	results  map[string]queue.Result
	groups   map[string]queue.GroupResult
	counters map[string]int
}

func NewMemoryBackend(options MemoryOptions) *MemoryBackend {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &MemoryBackend{
		now:      now,
		results:  map[string]queue.Result{},
		groups:   map[string]queue.GroupResult{},
		counters: map[string]int{},
	}
}

func (b *MemoryBackend) StoreResult(_ context.Context, result queue.Result) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if result.CreatedAt.IsZero() {
		result.CreatedAt = now
	}
	result.UpdatedAt = now
	b.results[result.TaskID] = result.Clone()
	return nil
}

func (b *MemoryBackend) GetResult(_ context.Context, taskID string) (queue.Result, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, ok := b.results[taskID]
	if !ok {
		return queue.Result{}, fmt.Errorf("%w: %s", ErrResultNotFound, taskID)
	}
	if result.ExpiresAt != nil && b.now().After(*result.ExpiresAt) {
		delete(b.results, taskID)
		return queue.Result{}, fmt.Errorf("%w: %s", ErrResultExpired, taskID)
	}
	return result.Clone(), nil
}

func (b *MemoryBackend) Forget(_ context.Context, taskID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.results, taskID)
	return nil
}

func (b *MemoryBackend) Wait(ctx context.Context, taskID string, timeout time.Duration) (queue.Result, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := b.GetResult(ctx, taskID)
		if err == nil && result.State.Terminal() {
			return result, nil
		}
		if err != nil && !errors.Is(err, ErrResultNotFound) {
			return queue.Result{}, err
		}
		if timeout > 0 && time.Now().After(deadline) {
			return queue.Result{}, fmt.Errorf("%w: wait timeout for %s", ErrResultNotFound, taskID)
		}
		select {
		case <-ctx.Done():
			return queue.Result{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (b *MemoryBackend) Children(ctx context.Context, taskID string) ([]string, error) {
	result, err := b.GetResult(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), result.Children...), nil
}

func (b *MemoryBackend) GroupResult(_ context.Context, groupID string, children []string) (queue.GroupResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	group := queue.GroupResult{ID: groupID, Children: append([]string(nil), children...), CreatedAt: b.now()}
	b.groups[groupID] = group.Clone()
	return group, nil
}

func (b *MemoryBackend) ChordCounter(_ context.Context, chordID string, delta int) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counters[chordID] += delta
	return b.counters[chordID], nil
}
