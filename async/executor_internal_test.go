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
