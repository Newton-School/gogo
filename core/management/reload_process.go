package management

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

const reloadWaitDelay = 2 * time.Second

type reloadProcess struct {
	command   *exec.Cmd
	interrupt bool // The Go tool handles Interrupt rather than SIGTERM.
	forced    bool
	done      chan struct{}
	err       error // Written once, then published by closing done.
}

func startReloadProcess(command *exec.Cmd) (*reloadProcess, error) {
	command.WaitDelay = reloadWaitDelay
	if err := command.Start(); err != nil {
		return nil, err
	}
	p := &reloadProcess{command: command, done: make(chan struct{})}
	go func() { p.err = command.Wait(); close(p.done) }()
	return p, nil
}

func (p *reloadProcess) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// stop requires an observed Wait completion. A failed signal, successful Kill,
// or elapsed deadline is not evidence that a replacement may be admitted.
func (p *reloadProcess) stop(grace time.Duration) error {
	if p == nil {
		return nil
	}
	if p.exited() {
		return p.err
	}
	var signalErr error
	if grace > 0 {
		if p.interrupt {
			signalErr = p.command.Process.Signal(os.Interrupt)
		} else {
			signalErr = reloadSignal(p.command.Process)
		}
		timer := time.NewTimer(grace)
		select {
		case <-p.done:
			timer.Stop()
			return p.err
		case <-timer.C:
		}
	}
	killErr := p.command.Process.Kill()
	p.forced = killErr == nil
	timer := time.NewTimer(reloadWaitDelay + time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.forced && reloadKilled(p.command.ProcessState) {
			return nil
		}
		return p.err
	case <-timer.C:
		if errors.Is(signalErr, os.ErrProcessDone) {
			signalErr = nil
		}
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return errors.Join(errors.New("owned process exit was not observed"), signalErr, killErr)
	}
}
