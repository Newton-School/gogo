package async

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func waitProcessExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}

// Linux does not expose a wait-for-all-descendants primitive to a library that
// must not become a process-global subreaper. Inspect readable procfs metadata
// while the unreaped leader pins the group. Restricted/unavailable metadata
// fails cleanup explicitly; it cannot be treated as proof that the group exited.
func inspectProcessGroup(pid int, deadline time.Time) (bool, error) {
	directory, err := os.Open("/proc")
	if err != nil {
		return false, err
	}
	defer directory.Close()
	leader, live := false, false
	for {
		entries, err := directory.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		for _, entry := range entries {
			if !time.Now().Before(deadline) {
				return false, errProcessGroupCleanup
			}
			member, numberErr := strconv.Atoi(entry.Name())
			if numberErr != nil || member <= 0 {
				continue
			}
			file, readErr := os.Open("/proc/" + entry.Name() + "/stat")
			if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, unix.ESRCH) {
				continue
			}
			if readErr != nil {
				return false, readErr
			}
			data, readErr := io.ReadAll(io.LimitReader(file, 8193))
			_ = file.Close()
			if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, unix.ESRCH) {
				continue
			}
			if readErr != nil {
				return false, readErr
			}
			state, parent, group, threads, parseErr := parseProcessStat(data)
			if parseErr != nil {
				return false, parseErr
			}
			if group != pid {
				continue
			}
			if member == pid {
				if parent != os.Getpid() {
					return false, unix.ECHILD
				}
				leader = true
			}
			// A dead thread-group leader can still have live sibling threads.
			// Do not mistake its own state for process-wide termination.
			if state != 'Z' && state != 'X' || threads > 1 {
				live = true
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if !leader {
		return false, unix.ECHILD
	}
	return !live, nil
}

func parseProcessStat(data []byte) (byte, int, int, int, error) {
	if len(data) > 8192 {
		return 0, 0, 0, 0, ErrUnavailable
	}
	// comm may contain spaces and closing parentheses; the last ')' ends it.
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return 0, 0, 0, 0, ErrUnavailable
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) < 18 || len(fields[0]) != 1 {
		return 0, 0, 0, 0, ErrUnavailable
	}
	parent, e1 := strconv.Atoi(fields[1])
	group, e2 := strconv.Atoi(fields[2])
	threads, e3 := strconv.Atoi(fields[17])
	if e1 != nil || e2 != nil || e3 != nil || parent < 0 || group < 0 || threads < 0 {
		return 0, 0, 0, 0, ErrUnavailable
	}
	return fields[0][0], parent, group, threads, nil
}
