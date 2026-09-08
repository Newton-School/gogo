package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const DefaultShutdownGrace = 30 * time.Second

var ErrClosed = errors.New("application is stopping or closed")

type lifecycleState uint8

const (
	prepared lifecycleState = iota
	starting
	running
	stopping
	closed
)

type cleanupHook struct {
	name string
	run  func(context.Context) error
}

// Application owns one startup and one shutdown. Do not copy it. Hooks must
// cooperate with context cancellation and must not call Start or Close on their
// own application. A timed-out hook may still be running: Go cannot kill it.
type Application struct {
	Registry *Registry
	ordered  []Config

	mu               sync.Mutex
	state            lifecycleState
	started          bool
	ready            bool
	admissionStopped bool
	startDone        chan struct{}
	startCancel      context.CancelFunc
	lifetime         context.Context
	startErr         error
	closeDone        chan struct{}
	closeErr         error
	// Only the startup owner writes these; the shutdown owner reads them after
	// startDone closes. No closer is lost if Open returns after Close times out.
	configs []cleanupHook
	closers []cleanupHook
}

func (a *Application) Start(ctx context.Context, resources []Resource) error {
	a.mu.Lock()
	if a.state >= stopping {
		a.mu.Unlock()
		return ErrClosed
	}
	if a.state != prepared {
		done := a.startDone
		a.mu.Unlock()
		select {
		case <-done:
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.state >= stopping {
				return errors.Join(a.startErr, ErrClosed)
			}
			return a.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	startCtx, cancel := context.WithCancel(ctx)
	a.startCancel, a.startDone, a.state = cancel, make(chan struct{}), starting
	a.lifetime = startCtx
	a.mu.Unlock()

	err := a.start(startCtx, resources)
	a.mu.Lock()
	if err == nil {
		err = startCtx.Err()
	}
	if err == nil && a.state >= stopping {
		err = ErrClosed
	}
	if err == nil {
		a.state, a.started, a.ready = running, true, !a.admissionStopped
	}
	a.startErr = err
	close(a.startDone)
	a.mu.Unlock()
	if err != nil {
		return errors.Join(err, a.Close(context.WithoutCancel(ctx)))
	}
	return nil
}

func (a *Application) start(ctx context.Context, resources []Resource) (err error) {
	// Validate the complete resource selection before opening the first one.
	seen := make(map[string]bool, len(resources))
	for _, r := range resources {
		if r.Name == "" || r.Open == nil || seen[r.Name] {
			return errors.New("invalid or duplicate resource")
		}
		seen[r.Name] = true
	}
	// Do not include arbitrary callback panic values in public errors.
	defer func() {
		if recover() != nil {
			err = errors.New("application startup callback panicked")
		}
	}()
	for _, r := range resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		closer, err := r.Open(ctx)
		if closer != nil {
			a.closers = append(a.closers, cleanupHook{"resource " + r.Name, closer})
		}
		if err != nil {
			return fmt.Errorf("open resource %s: %w", r.Name, err)
		}
	}
	for _, c := range a.ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.Shutdown != nil {
			a.configs = append(a.configs, cleanupHook{"app " + c.Label, c.Shutdown})
		}
		if c.Ready != nil {
			if err := c.Ready(ctx, a.Registry); err != nil {
				return fmt.Errorf("ready app %s: %w", c.Label, err)
			}
		}
	}
	return nil
}

// Ready also rejects a canceled lifetime context. The command/server owner must
// still drain its work before calling Close; cancellation alone must not close
// shared pools out from under an HTTP or worker drain.
func (a *Application) Ready() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ready && a.lifetime != nil && a.lifetime.Err() == nil
}

func (a *Application) StopAdmission() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.admissionStopped, a.ready = true, false
}

// Close stops admission immediately, cancels startup if needed, and invokes all
// acquired cleanup hooks once in reverse order. The first caller owns cleanup's
// deadline (DefaultShutdownGrace when absent); subsequent callers only wait for it.
// A timeout bounds this call, not arbitrary user goroutines. Remaining callbacks
// are still attempted with the expired context. Late startup resources retain an
// owner that attempts their cleanup when startup eventually returns.
func (a *Application) Close(ctx context.Context) error {
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		ctx, cancel = context.WithCancel(ctx)
	} else {
		ctx, cancel = context.WithTimeout(ctx, DefaultShutdownGrace)
	}
	defer cancel()
	a.mu.Lock()
	a.admissionStopped, a.ready = true, false
	if a.closeDone == nil {
		if a.startDone == nil {
			a.startDone = make(chan struct{})
			close(a.startDone)
		}
		if a.startCancel != nil {
			a.startCancel()
		}
		a.closeDone, a.state = make(chan struct{}), stopping
		go a.close(ctx)
	}
	done := a.closeDone
	a.mu.Unlock()
	select {
	case <-done:
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.closeErr
	default:
	}
	select {
	case <-done:
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Application) close(ctx context.Context) {
	<-a.startDone
	var errs []error
	for _, hooks := range [][]cleanupHook{a.configs, a.closers} {
		for i := len(hooks) - 1; i >= 0; i-- {
			if err := boundedCleanup(ctx, hooks[i]); err != nil {
				errs = append(errs, fmt.Errorf("close %s: %w", hooks[i].name, err))
			}
		}
	}
	a.mu.Lock()
	a.closeErr, a.state = errors.Join(errs...), closed
	close(a.closeDone)
	a.mu.Unlock()
}

func boundedCleanup(ctx context.Context, hook cleanupHook) error {
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if recover() != nil {
				err = errors.New("application cleanup callback panicked")
			}
			done <- err
		}()
		err = hook.run(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
