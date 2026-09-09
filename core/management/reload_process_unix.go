//go:build darwin || linux

package management

import (
	"os"
	"syscall"
)

const reloadSupported = true

// Signal only the process represented by this owned os.Process handle. There
// is intentionally no process-group discovery or descendant signalling here.
func reloadSignal(process *os.Process) error { return process.Signal(syscall.SIGTERM) }

func reloadKilled(state *os.ProcessState) bool {
	// Wait has already completed. Inspect its concrete OS outcome, never an
	// output writer's error tree or a provider-supplied ExitError lookalike.
	if state == nil {
		return false
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}
