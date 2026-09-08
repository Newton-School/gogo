//go:build darwin || linux

package async

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIsolationExitHelper(t *testing.T) {
	if os.Getenv("GOGO_TEST_ISOLATION_EXIT") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(`{"output":42}`)
	os.Exit(0)
}

func TestIsolatedSuccessfulExitPreservesStatus(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestIsolationExitHelper$")
	cmd.Env = []string{"GOGO_TEST_ISOLATION_EXIT=1", "GORACE=atexit_sleep_ms=0"}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.WaitDelay = 100 * time.Millisecond
	if err := runIsolatedProcess(cmd); err != nil {
		t.Fatal(err, out.String())
	}
}

func TestProcessGroupCleanupRequiresObservationWithinBound(t *testing.T) {
	count := 0
	signal := func(int) error { count++; return nil }
	inspect := func(int, time.Time) (bool, error) { return count >= 3, nil }
	if err := drainOwnedProcessGroup(1, time.Second, inspect, signal); err != nil || count != 3 {
		t.Fatal(err, count)
	}
	start := time.Now()
	if err := drainOwnedProcessGroup(1, 10*time.Millisecond, func(int, time.Time) (bool, error) { return false, nil }, signal); !errors.Is(err, errProcessGroupCleanup) || time.Since(start) > time.Second {
		t.Fatal(err)
	}
	if err := drainOwnedProcessGroup(1, time.Second, func(int, time.Time) (bool, error) { return false, syscall.EACCES }, signal); !errors.Is(err, syscall.EACCES) {
		t.Fatal(err)
	}
	if err := drainOwnedProcessGroup(1, time.Second, func(int, time.Time) (bool, error) { return false, nil }, func(int) error { return syscall.EPERM }); !errors.Is(err, syscall.EPERM) {
		t.Fatal(err)
	}
	if err := drainOwnedProcessGroup(1, time.Second, func(int, time.Time) (bool, error) { return true, nil }, func(int) error { return syscall.EPERM }); err != nil {
		t.Fatal("zombie-only EPERM", err)
	}
	if err := drainOwnedProcessGroup(0, time.Second, inspect, func(int) error { t.Fatal("invalid PID signaled"); return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := drainOwnedProcessGroup(1, 5*time.Millisecond, func(int, time.Time) (bool, error) { time.Sleep(10 * time.Millisecond); return true, nil }, signal); !errors.Is(err, errProcessGroupCleanup) {
		t.Fatal("late observation claimed within-budget cleanup", err)
	}
}

func TestProcessGroupCleanupRetriesPermissionDenialWithinOriginalBound(t *testing.T) {
	t.Run("transition", func(t *testing.T) {
		calls := 0
		var firstDeadline time.Time
		err := drainOwnedProcessGroup(1, 100*time.Millisecond, func(_ int, deadline time.Time) (bool, error) {
			if firstDeadline.IsZero() {
				firstDeadline = deadline
			} else if deadline != firstDeadline {
				t.Fatal("cleanup deadline was extended")
			}
			return calls == 2, nil
		}, func(int) error { calls++; return syscall.EPERM })
		if err != nil || calls != 2 {
			t.Fatal("transitional denial prevented observed exit", err, calls)
		}
	})
	t.Run("persistent", func(t *testing.T) {
		calls := 0
		started := time.Now()
		err := drainOwnedProcessGroup(1, 10*time.Millisecond, func(int, time.Time) (bool, error) { return false, nil }, func(int) error {
			calls++
			return syscall.EPERM
		})
		if !errors.Is(err, syscall.EPERM) || !errors.Is(err, errProcessGroupCleanup) || calls < 2 || time.Since(started) > time.Second {
			t.Fatal("persistent denial lost its cause or cleanup bound", err, calls)
		}
	})
	t.Run("denial retained until confirmed exit", func(t *testing.T) {
		calls := 0
		err := drainOwnedProcessGroup(1, 10*time.Millisecond, func(int, time.Time) (bool, error) { return false, nil }, func(int) error {
			calls++
			if calls == 1 {
				return syscall.EPERM
			}
			return nil
		})
		if !errors.Is(err, syscall.EPERM) || !errors.Is(err, errProcessGroupCleanup) || calls < 2 {
			t.Fatal("later signal success erased unconfirmed denial", err, calls)
		}
	})
	t.Run("inspection failure", func(t *testing.T) {
		calls := 0
		err := drainOwnedProcessGroup(1, time.Second, func(int, time.Time) (bool, error) { return false, syscall.ECHILD }, func(int) error {
			calls++
			return syscall.EPERM
		})
		if !errors.Is(err, syscall.EPERM) || !errors.Is(err, syscall.ECHILD) || calls != 1 {
			t.Fatal("ownership failure was retried or lost", err, calls)
		}
	})
	t.Run("other signal failure", func(t *testing.T) {
		calls := 0
		err := drainOwnedProcessGroup(1, time.Second, func(int, time.Time) (bool, error) { return false, nil }, func(int) error {
			calls++
			return syscall.EACCES
		})
		if !errors.Is(err, syscall.EACCES) || calls != 1 {
			t.Fatal("unrelated signal failure was retried", err, calls)
		}
	})
	t.Run("late observation", func(t *testing.T) {
		err := drainOwnedProcessGroup(1, 5*time.Millisecond, func(int, time.Time) (bool, error) {
			time.Sleep(10 * time.Millisecond)
			return true, nil
		}, func(int) error { return syscall.EPERM })
		if !errors.Is(err, syscall.EPERM) || !errors.Is(err, errProcessGroupCleanup) {
			t.Fatal("late zombie observation cleared timeout or denial", err)
		}
	})
}

func TestRawProcessTreeExitDiagnostics(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"quick", "closed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ready := filepath.Join(t.TempDir(), "ready")
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestProcessTreeHelper$")
			cmd.Env = []string{"GOGO_TEST_TREE_MODE=" + mode, "GOGO_TEST_TREE_READY=" + ready, "GORACE=atexit_sleep_ms=0"}
			payload, _ := json.Marshal(Execution{})
			cmd.Stdin = bytes.NewReader(payload)
			cmd.Stdout = &boundedBuffer{limit: MaxPayloadBytes}
			cmd.Stderr = io.Discard
			cmd.WaitDelay = 100 * time.Millisecond
			if err := runIsolatedProcess(cmd); err != nil {
				t.Fatal("isolated exit cause", err)
			}
		})
	}
}

// Match the public immediate-exit regression's reused executor configuration,
// path and one-second deadline while retaining the private process error.
func TestRawImmediateExitLoopDiagnostics(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "grandchild.ready")
	executor := &ProcessExecutor{
		Command:          []string{binary, "-test.run=^TestProcessTreeHelper$"},
		Environment:      []string{"GOGO_TEST_TREE_MODE=quick", "GOGO_TEST_TREE_READY=" + ready, "GORACE=atexit_sleep_ms=0"},
		PipeDrainTimeout: 100 * time.Millisecond,
	}
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		maxBytes, drain, err := executor.limits()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		payload, err := json.Marshal(Execution{})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, executor.Command[0], executor.Command[1:]...)
		cmd.WaitDelay = drain
		cmd.Env = append([]string(nil), executor.Environment...)
		cmd.Stdin = bytes.NewReader(payload)
		stdout := &boundedBuffer{limit: maxBytes}
		cmd.Stdout = stdout
		cmd.Stderr = io.Discard
		err = runIsolatedProcess(cmd)
		contextErr := ctx.Err()
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: isolated exit cause: %v; context: %v", i, err, contextErr)
		}
		var response childResponse
		if err := decodeJSON(stdout.Bytes(), &response); err != nil || string(response.Output) != "42" {
			t.Fatalf("iteration %d: output %q: %v", i, response.Output, err)
		}
	}
}
