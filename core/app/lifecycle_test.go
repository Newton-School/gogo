package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloseBeforeStartIsTerminal(t *testing.T) {
	a, _ := Prepare(nil, nil)
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := a.Start(context.Background(), []Resource{{Name: "unexpected", Open: func(context.Context) (func(context.Context) error, error) {
		t.Error("opened after shutdown")
		return nil, nil
	}}})
	if !errors.Is(err, ErrClosed) || a.Ready() {
		t.Fatalf("start after shutdown: %v", err)
	}
}

func TestResourceSelectionValidatedBeforeOpening(t *testing.T) {
	a, _ := Prepare(nil, nil)
	resource := Resource{Name: "duplicate", Open: func(context.Context) (func(context.Context) error, error) {
		t.Error("opened before complete resource validation")
		return nil, nil
	}}
	if err := a.Start(context.Background(), []Resource{resource, resource}); err == nil {
		t.Fatal("accepted duplicate resource")
	}
}

func TestStartupContextLivesUntilShutdown(t *testing.T) {
	var resourceCtx context.Context
	a, err := Bootstrap(context.Background(), nil, []Resource{{Name: "resource", Open: func(ctx context.Context) (func(context.Context) error, error) {
		resourceCtx = ctx
		return nil, nil
	}}}, nil)
	if err != nil || resourceCtx.Err() != nil {
		t.Fatalf("startup context canceled prematurely: %v / %v", err, resourceCtx.Err())
	}
	_ = a.Close(context.Background())
	if resourceCtx.Err() == nil {
		t.Fatal("shutdown did not cancel the owned context")
	}
}

func TestCloseDuringStartupCancelsAndCleansLateResource(t *testing.T) {
	a, _ := Prepare(nil, nil)
	opened, release, cleaned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	startResult := make(chan error, 1)
	go func() {
		startResult <- a.Start(context.Background(), []Resource{{Name: "late", Open: func(ctx context.Context) (func(context.Context) error, error) {
			close(opened)
			<-release // Deliberately non-cooperative third-party callback.
			if ctx.Err() == nil {
				t.Error("startup was not canceled")
			}
			return func(context.Context) error { close(cleaned); return nil }, nil
		}}})
	}()
	<-opened
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded startup should report shutdown timeout: %v", err)
	}
	if a.Ready() {
		t.Fatal("closing application became ready")
	}
	close(release)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("late resource lost its cleanup owner")
	}
	if err := <-startResult; err == nil || a.Ready() {
		t.Fatalf("startup raced past shutdown: %v", err)
	}
}

func TestShutdownTimeoutStillAttemptsEveryCleanup(t *testing.T) {
	blocked, release, later := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a, err := Bootstrap(context.Background(), []Config{{Name: "app", Label: "app", Shutdown: func(context.Context) error {
		close(blocked)
		<-release
		return nil
	}}}, []Resource{{Name: "database", Open: func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { close(later); return nil }, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocking hook did not time out: %v", err)
	}
	<-blocked
	select {
	case <-later:
	case <-time.After(time.Second):
		t.Fatal("timeout skipped remaining resource cleanup")
	}
	close(release)
}

func TestCleanupPanicIsRedactedAndDoesNotSkipResources(t *testing.T) {
	var count atomic.Int32
	a, err := Bootstrap(context.Background(), []Config{{Name: "app", Label: "app", Shutdown: func(context.Context) error {
		panic("secret callback detail")
	}}}, []Resource{{Name: "db", Open: func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { count.Add(1); return nil }, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			err := a.Close(context.Background())
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Errorf("missing or unredacted panic: %v", err)
			}
		})
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("resource closed %d times", count.Load())
	}
}

func TestStartupPanicCleansAlreadyOpenedResources(t *testing.T) {
	var count int
	_, err := Bootstrap(context.Background(), []Config{{Name: "app", Label: "app", Ready: func(context.Context, *Registry) error {
		panic("secret")
	}}}, []Resource{{Name: "db", Open: func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { count++; return nil }, nil
	}}}, nil)
	if err == nil || strings.Contains(err.Error(), "secret") || count != 1 {
		t.Fatalf("panic cleanup: %v count=%d", err, count)
	}
}

func TestStopAdmissionIsNotOverwrittenByStartup(t *testing.T) {
	a, _ := Prepare(nil, nil)
	a.StopAdmission()
	if err := a.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if a.Ready() {
		t.Fatal("startup reopened admission")
	}
	_ = a.Close(context.Background())
}

func TestLifetimeCancellationStopsReadinessBeforeOwnerDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cleaned atomic.Bool
	a, err := Bootstrap(ctx, nil, []Resource{{Name: "db", Open: func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { cleaned.Store(true); return nil }, nil
	}}}, nil)
	if err != nil || !a.Ready() {
		t.Fatalf("not ready after startup: %v", err)
	}
	cancel()
	if a.Ready() || cleaned.Load() {
		t.Fatal("cancellation retained readiness or closed resources before owner drain")
	}
	if err := a.Close(context.Background()); err != nil || !cleaned.Load() {
		t.Fatal("owner cleanup failed", err)
	}
}
