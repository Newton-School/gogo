package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertStatus(t *testing.T, a *Application, want Status) {
	t.Helper()
	if got := a.Status(); got != want {
		t.Fatalf("Status() = %+v, want %+v", got, want)
	}
}

func TestStatusNilPreparedAndCloseBeforeStart(t *testing.T) {
	assertStatus(t, nil, Status{Stopping: true, Closed: true, Failed: true})
	a, err := Prepare(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, a, Status{})
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, a, Status{Stopping: true, Closed: true})
	if err := a.Start(context.Background(), nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	assertStatus(t, a, Status{Stopping: true, Closed: true})
}

func TestStatusStartupAndShutdownPhases(t *testing.T) {
	resourceEntered, resourceRelease := make(chan struct{}), make(chan struct{})
	hookEntered, hookRelease := make(chan struct{}), make(chan struct{})
	cleanupEntered, cleanupRelease := make(chan struct{}), make(chan struct{})
	a, err := Prepare([]Config{{Name: "example", Label: "example", Ready: func(context.Context, *Registry) error {
		close(hookEntered)
		<-hookRelease
		return nil
	}, Shutdown: func(context.Context) error {
		close(cleanupEntered)
		<-cleanupRelease
		return nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() {
		started <- a.Start(context.Background(), []Resource{{Name: "resource", Open: func(context.Context) (func(context.Context) error, error) {
			close(resourceEntered)
			<-resourceRelease
			return nil, nil
		}}})
	}()
	<-resourceEntered
	assertStatus(t, a, Status{})
	close(resourceRelease)
	<-hookEntered
	assertStatus(t, a, Status{})
	close(hookRelease)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	assertStatus(t, a, Status{Started: true, Ready: true})
	closedResult := make(chan error, 1)
	go func() { closedResult <- a.Close(context.Background()) }()
	<-cleanupEntered
	assertStatus(t, a, Status{Started: true, Stopping: true})
	close(cleanupRelease)
	if err := <-closedResult; err != nil {
		t.Fatal(err)
	}
	assertStatus(t, a, Status{Started: true, Stopping: true, Closed: true})
}

func TestStatusStopAdmissionDoesNotEraseCompletedStartup(t *testing.T) {
	for _, beforeStart := range []bool{false, true} {
		a, _ := Prepare(nil, nil)
		if beforeStart {
			a.StopAdmission()
			assertStatus(t, a, Status{Stopping: true})
		}
		if err := a.Start(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		a.StopAdmission()
		assertStatus(t, a, Status{Started: true, Stopping: true})
		if err := a.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatusCanceledLifetimeDoesNotRunCleanupOrMutateLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var opened, cleaned atomic.Int32
	a, err := Bootstrap(ctx, nil, []Resource{{Name: "resource", Open: func(context.Context) (func(context.Context) error, error) {
		opened.Add(1)
		return func(context.Context) error { cleaned.Add(1); return nil }, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range 10 {
		assertStatus(t, a, Status{Started: true, Stopping: true})
	}
	a.mu.Lock()
	unchanged := a.state == running && a.ready && !a.admissionStopped
	a.mu.Unlock()
	if !unchanged || opened.Load() != 1 || cleaned.Load() != 0 {
		t.Fatal("status changed lifecycle or invoked a resource callback")
	}
	if err := a.Close(context.Background()); err != nil || cleaned.Load() != 1 {
		t.Fatalf("owner cleanup: %v, calls %d", err, cleaned.Load())
	}
}

func TestStatusStartupAndCleanupFailures(t *testing.T) {
	for _, panicHook := range []bool{false, true} {
		failure := func(context.Context, *Registry) error {
			if panicHook {
				panic("private startup detail")
			}
			return errors.New("private startup detail")
		}
		a, _ := Prepare([]Config{{Name: "example", Label: "example", Ready: failure}}, nil)
		if err := a.Start(context.Background(), nil); err == nil {
			t.Fatal("expected startup failure")
		}
		assertStatus(t, a, Status{Stopping: true, Closed: true, Failed: true})

		a, _ = Prepare([]Config{{Name: "example", Label: "example", Shutdown: func(ctx context.Context) error {
			return failure(ctx, nil)
		}}}, nil)
		if err := a.Start(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(context.Background()); err == nil {
			t.Fatal("expected cleanup failure")
		}
		assertStatus(t, a, Status{Started: true, Stopping: true, Closed: true, Failed: true})
	}
}

type statusContext struct {
	context.Context
	err func() error
}

func (c *statusContext) Err() error { return c.err() }

func TestStatusContextCallbacksRunOutsideMutexAndFenceAdmission(t *testing.T) {
	a := &Application{state: running, started: true, ready: true}
	a.lifetime = &statusContext{Context: context.Background(), err: func() error {
		a.StopAdmission()
		return nil
	}}
	done := make(chan Status, 1)
	go func() { done <- a.Status() }()
	select {
	case got := <-done:
		if got != (Status{Started: true, Stopping: true}) {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("Status held lifecycle mutex during a context callback")
	}
}

func TestStatusInvalidContextAndOpaqueErrorsFailClosed(t *testing.T) {
	var typedNil *statusContext
	for _, ctx := range []context.Context{typedNil, &statusContext{err: func() error { panic("private context detail") }}} {
		a := &Application{state: running, started: true, ready: true, lifetime: ctx}
		assertStatus(t, a, Status{Started: true, Stopping: true, Failed: true})
		if a.state != running || !a.ready || a.admissionStopped {
			t.Fatal("failed snapshot changed lifecycle")
		}
	}
	a := &Application{startErr: statusOpaqueError{}, closeErr: statusOpaqueError{}}
	assertStatus(t, a, Status{Failed: true})
}

type statusOpaqueError struct{}

func (statusOpaqueError) Error() string { panic("status must not format internal errors") }

func TestStatusConcurrentLifecycleReads(t *testing.T) {
	a, _ := Prepare(nil, nil)
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 1000 {
				status := a.Status()
				if status.Ready && (!status.Started || status.Stopping || status.Closed || status.Failed) || status.Closed && !status.Stopping {
					t.Errorf("inconsistent snapshot: %+v", status)
					return
				}
			}
		})
	}
	if err := a.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	a.StopAdmission()
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	assertStatus(t, a, Status{Started: true, Stopping: true, Closed: true})
}
