package async

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const darwinZombie = 5 // SZOMB, the stable Darwin kern.proc process-state ABI.

func waitProcessExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	unix.CloseOnExec(kq)
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	for {
		_, err = unix.Kevent(kq, []unix.Kevent_t{change}, nil, &unix.Timespec{})
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	// NOTE_EXIT is edge-triggered. A fast child can exit before registration;
	// checking its still-unreaped zombie status after registering closes that
	// race without consuming wait status or allowing PID reuse.
	info, inspectErr := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if inspectErr != nil {
		return inspectErr
	}
	if info.Proc.P_pid != int32(pid) || info.Eproc.Ppid != int32(os.Getpid()) {
		return unix.ECHILD
	}
	if info.Proc.P_stat == darwinZombie {
		return nil
	}
	if err != nil {
		return err
	}
	events := make([]unix.Kevent_t, 1)
	for {
		count, err := unix.Kevent(kq, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 1 && events[0].Flags&unix.EV_ERROR != 0 {
			return unix.Errno(events[0].Data)
		}
		if count == 1 && events[0].Ident == uint64(pid) && events[0].Fflags&unix.NOTE_EXIT != 0 {
			return nil
		}
	}
}

func inspectProcessGroup(pid int, _ time.Time) (bool, error) {
	// Darwin reports EPERM for a group containing only unsignalable zombies.
	// Distinguish it from a live descendant whose credentials deny signalling;
	// never swallow that genuine cleanup failure.
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if err != nil {
		return false, err
	}
	leader, live := false, false
	for _, process := range processes {
		if process.Proc.P_pid == int32(pid) {
			if process.Eproc.Ppid != int32(os.Getpid()) {
				return false, unix.ECHILD
			}
			leader = true
		}
		if process.Proc.P_stat != darwinZombie {
			live = true
		}
	}
	if !leader {
		return false, unix.ECHILD
	}
	return !live, nil
}
