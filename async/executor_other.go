//go:build !darwin && !linux

package async

import "os/exec"

const processIsolationSupported = false

// Other platforms require an explicit process-tree implementation (for example
// Windows Job Objects). Do not silently claim direct-child kill is equivalent.
func runIsolatedProcess(*exec.Cmd) error { return ErrUnavailable }
