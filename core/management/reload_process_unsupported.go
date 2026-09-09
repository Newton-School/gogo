//go:build !darwin && !linux

package management

import (
	"errors"
	"os"
)

const reloadSupported = false

func reloadSignal(*os.Process) error { return errors.New("reload process supervision is unsupported") }

func reloadKilled(*os.ProcessState) bool { return false }
