//go:build darwin || linux

package async_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

// A real grandchild inherits the task's output pipes. The listener proves
// whether it remains alive without confusing a reaped process with a zombie.
func TestProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("GOGO_TEST_TREE_MODE")
	if mode == "" {
		return
	}
	if mode == "quick" {
		_, _ = os.Stdout.WriteString(`{"output":42}`)
		os.Exit(0)
	}
	ready := os.Getenv("GOGO_TEST_TREE_READY")
	if mode == "grandchild" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		time.AfterFunc(10*time.Second, func() { os.Exit(0) })
		if err := os.WriteFile(ready, []byte(listener.Addr().String()), 0600); err != nil {
			os.Exit(2)
		}
		for {
			connection, err := listener.Accept()
			if err != nil {
				os.Exit(0)
			}
			_ = connection.SetReadDeadline(time.Now().Add(time.Second))
			var command [1]byte
			_, _ = connection.Read(command[:])
			_ = connection.Close()
			if command[0] == 'q' {
				os.Exit(0)
			}
		}
	}
	binary, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	child := exec.Command(binary, "-test.run=^TestProcessTreeHelper$")
	child.Env = []string{"GOGO_TEST_TREE_MODE=grandchild", "GOGO_TEST_TREE_READY=" + ready, "GORACE=atexit_sleep_ms=0"}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if mode == "closed" {
		child.Stdout, child.Stderr = nil, nil
	}
	if mode == "escaped" {
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if mode == "escaped" || mode == "exit" || mode == "closed" || mode == "failure" {
		for deadline := time.Now().Add(4 * time.Second); ; {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(2)
			}
			time.Sleep(time.Millisecond)
		}
		_, _ = os.Stdout.WriteString(`{"output":42}`)
		if mode == "failure" {
			os.Exit(2)
		}
		os.Exit(0)
	}
	_ = child.Wait()
	os.Exit(0)
}

func processTreeExecutor(t *testing.T, mode string) (*async.ProcessExecutor, string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "grandchild.ready")
	// Race's default one-second exit delay is unrelated to pipe draining.
	return &async.ProcessExecutor{Command: []string{binary, "-test.run=^TestProcessTreeHelper$"}, Environment: []string{"GOGO_TEST_TREE_MODE=" + mode, "GOGO_TEST_TREE_READY=" + ready, "GORACE=atexit_sleep_ms=0"}, PipeDrainTimeout: 100 * time.Millisecond}, ready
}

func waitGrandchild(t *testing.T, ready string) string {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if value, err := os.ReadFile(ready); err == nil && len(value) > 0 {
			address := string(value)
			t.Cleanup(func() {
				if c, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
					_, _ = c.Write([]byte("q"))
					_ = c.Close()
				}
			})
			return address
		}
		select {
		case <-deadline.C:
			t.Fatal("grandchild did not start")
		case <-tick.C:
		}
	}
}

func TestProcessExecutorCancelsOwnedGrandchildGroup(t *testing.T) {
	executor, ready := processTreeExecutor(t, "parent")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := executor.Execute(ctx, async.Execution{}); done <- err }()
	address := waitGrandchild(t, ready)
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("inherited pipes blocked cancellation")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatal(elapsed)
	}
	deadline := time.Now().Add(time.Second)
	for {
		c, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("ordinary grandchild survived group cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcessExecutorBoundsPipesWhenDescendantEscapesSession(t *testing.T) {
	executor, ready := processTreeExecutor(t, "escaped")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := executor.Execute(ctx, async.Execution{}); done <- err }()
	_ = waitGrandchild(t, ready)
	select {
	case err := <-done:
		var failure async.Failure
		if !errors.As(err, &failure) || failure.Code != "CHILD_FAILED" || ctx.Err() != nil {
			t.Fatal("unclosed pipes fabricated success or waited for task deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("escaped descendant retained output pipes indefinitely")
	}
}

func TestProcessExecutorCleansGroupBeforeReapingExitedParent(t *testing.T) {
	for _, mode := range []string{"exit", "closed", "failure"} {
		t.Run(mode, func(t *testing.T) {
			executor, ready := processTreeExecutor(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				output, err := executor.Execute(ctx, async.Execution{})
				if err == nil && string(output) != "42" {
					err = async.ErrInvalid
				}
				done <- err
			}()
			address := waitGrandchild(t, ready)
			select {
			case err := <-done:
				if mode == "failure" {
					var failure async.Failure
					if !errors.As(err, &failure) || failure.Code != "CHILD_FAILED" {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("early parent exit did not settle")
			}
			if ctx.Err() != nil {
				t.Fatal("cleanup waited for task deadline")
			}
			if c, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
				_ = c.Close()
				t.Fatal("descendant survived completed invocation")
			}
		})
	}
}

func TestProcessExecutorCancellationDoesNotSignalAnotherInvocation(t *testing.T) {
	first, firstReady := processTreeExecutor(t, "parent")
	second, secondReady := processTreeExecutor(t, "parent")
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := first.Execute(firstCtx, async.Execution{}); firstDone <- err }()
	go func() { _, err := second.Execute(secondCtx, async.Execution{}); secondDone <- err }()
	_ = waitGrandchild(t, firstReady)
	address := waitGrandchild(t, secondReady)
	firstCancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first cancellation stalled")
	}
	c, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err != nil {
		t.Fatal("separate invocation was killed", err)
	}
	_ = c.Close()
	select {
	case err := <-secondDone:
		t.Fatal("unrelated invocation exited", err)
	default:
	}
	secondCancel()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second cancellation stalled")
	}
}

func TestProcessExecutorImmediateExitDoesNotMissExitObservation(t *testing.T) {
	executor, _ := processTreeExecutor(t, "quick")
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		output, err := executor.Execute(ctx, async.Execution{})
		cancel()
		if err != nil || string(output) != "42" {
			t.Fatal(i, string(output), err)
		}
	}
}
