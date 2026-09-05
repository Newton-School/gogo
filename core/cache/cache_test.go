package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	mu      sync.Mutex
	value   []byte
	failure error
}

func (f *fake) Get(context.Context, string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failure != nil {
		return nil, f.failure
	}
	if f.value == nil {
		return nil, ErrMiss
	}
	return append([]byte(nil), f.value...), nil
}
func (f *fake) Set(_ context.Context, _ string, v []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = append([]byte(nil), v...)
	return f.failure
}
func (f *fake) Add(ctx context.Context, k string, v []byte, d time.Duration) (bool, error) {
	return true, f.Set(ctx, k, v, d)
}
func (f *fake) Delete(context.Context, string) error                    { return nil }
func (f *fake) Increment(context.Context, string, int64) (int64, error) { return 0, nil }
func TestBypassExecutesLoaderAndNeverCachesFailure(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		f := &fake{failure: errors.New("unavailable")}
		c := Cache{Store: f, BypassUnavailable: bypass}
		calls := 0
		v, e := c.GetOrSet(context.Background(), "x", func(context.Context) ([]byte, error) { calls++; return []byte("loaded"), nil })
		if bypass && (e != nil || string(v) != "loaded" || calls != 1) {
			t.Fatal(v, e, calls)
		}
		if !bypass && (e == nil || calls != 0) {
			t.Fatal(e, calls)
		}
	}
}
func TestConcurrentSingleLoader(t *testing.T) {
	var calls atomic.Int32
	c := Cache{Store: &fake{}}
	var wg sync.WaitGroup
	release := make(chan struct{})
	started := make(chan struct{})
	loader := func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("ok"), nil
	}
	for range 30 {
		wg.Go(func() {
			v, e := c.GetOrSet(context.Background(), "key", loader)
			if e != nil || string(v) != "ok" {
				t.Error(e)
			}
		})
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
