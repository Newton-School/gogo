//go:build darwin || linux

package async

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
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
		// SIGKILL delivery is asynchronous. Do not release this invocation's
		// slot after merely sending it: observe ordinary group members exit
		// while the unreaped leader still pins the process-group identity.
		cleanupErr = drainOwnedProcessGroup(cmd.Process.Pid, 250*time.Millisecond, inspectProcessGroup, func(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) })
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

var errProcessGroupCleanup = errors.New("task process-group cleanup not observed before deadline")

func drainOwnedProcessGroup(pid int, timeout time.Duration, inspect func(int, time.Time) (bool, error), signal func(int) error) error {
	if pid <= 0 || timeout <= 0 {
		return ErrInvalid
	}
	deadline := time.Now().Add(timeout)
	for {
		if !time.Now().Before(deadline) {
			return errProcessGroupCleanup
		}
		signalErr := signal(pid)
		exited, err := inspect(pid, deadline)
		if !time.Now().Before(deadline) {
			return errors.Join(errProcessGroupCleanup, signalErr, err)
		}
		if err != nil {
			return errors.Join(signalErr, err)
		}
		if exited {
			return nil
		} // Darwin may return EPERM for only zombies.
		if signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) {
			return signalErr
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errProcessGroupCleanup
		}
		time.Sleep(min(time.Millisecond, remaining))
	}
}
