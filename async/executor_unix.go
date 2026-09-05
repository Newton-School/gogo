//go:build darwin || linux

package async

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

const processIsolationSupported = true

func runIsolatedProcess(cmd *exec.Cmd) error {
	// A new group makes the task and ordinary descendants independent of the
	// worker's group. Never signal the worker's inherited process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var mu sync.Mutex
	released := false
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if released || cmd.Process == nil || cmd.Process.Pid <= 0 {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Observe exit without reaping. The leader's PID remains reserved, so
	// group cleanup cannot accidentally signal a reused PID/process group.
	observeErr := waitProcessExit(cmd.Process.Pid)
	if observeErr != nil {
		observeErr = fmt.Errorf("observe task process exit: %w", observeErr)
	}
	mu.Lock()
	var cleanupErr error
	if observeErr == nil {
		cleanupErr = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(cleanupErr, syscall.ESRCH) {
			cleanupErr = nil
		}
		if errors.Is(cleanupErr, syscall.EPERM) && groupHasOnlyExitedProcesses(cmd.Process.Pid) {
			cleanupErr = nil
		}
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("clean task process group: %w", cleanupErr)
		}
	} else {
		// If exit ownership cannot be established, do not send a group
		// signal. The direct child's os.Process handle is still bounded by
		// Cmd.WaitDelay and the context; surface the observer failure.
		_ = cmd.Process.Kill()
	}
	released = true
	mu.Unlock()
	return errors.Join(observeErr, cleanupErr, cmd.Wait())
}
