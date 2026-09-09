//go:build darwin || linux

package management

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestReloadProcessHelper(t *testing.T) {
	mode := os.Getenv("GOGO_TEST_RELOAD_PROCESS")
	if mode == "" {
		return
	}
	if mode == "ignore" {
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
	} else {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		_, _ = io.WriteString(os.Stdout, "ready\n")
		<-signals
		if mode == "reject" {
			os.Exit(7)
		}
		os.Exit(0)
	}
	_, _ = io.WriteString(os.Stdout, "ready\n")
	for {
		time.Sleep(time.Hour)
	}
}

type reloadReadyWriter struct {
	ready   chan struct{}
	once    sync.Once
	failure error
}

func (w *reloadReadyWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "ready") {
		w.once.Do(func() { close(w.ready) })
	}
	if w.failure != nil {
		return 0, w.failure
	}
	return len(p), nil
}

func reloadTestProcess(t *testing.T, mode string, failure error) *reloadProcess {
	t.Helper()
	writer := &reloadReadyWriter{ready: make(chan struct{}), failure: failure}
	command := exec.Command(os.Args[0], "-test.run=^TestReloadProcessHelper$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOGO_TEST_RELOAD_PROCESS=") && !strings.HasPrefix(entry, "GORACE=") {
			command.Env = append(command.Env, entry)
		}
	}
	// Preserve race detection, but exclude its default one-second exit sleep
	// from the application's deliberately short graceful-shutdown fixture.
	command.Env = append(command.Env, "GOGO_TEST_RELOAD_PROCESS="+mode, "GORACE=atexit_sleep_ms=0")
	command.Stdout, command.Stderr = writer, io.Discard
	p, err := startReloadProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.stop(0) })
	select {
	case <-writer.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("owned child never reached signal-ready witness")
	}
	return p
}

func TestReloadProcessStopObservesGracefulAndForcedExit(t *testing.T) {
	for _, mode := range []string{"cooperate", "reject", "ignore"} {
		t.Run(mode, func(t *testing.T) {
			p := reloadTestProcess(t, mode, nil)
			err := p.stop(50 * time.Millisecond)
			if !p.exited() {
				t.Fatal("stop returned without Wait completion")
			}
			if mode == "reject" {
				if err == nil {
					t.Fatal("cleanup failure authorized replacement")
				}
				if reloadKilled(p.command.ProcessState) {
					t.Fatal("ordinary failed exit was classified as a forced kill")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.forced != (mode == "ignore") {
				t.Fatalf("forced=%v for %s", p.forced, mode)
			}
			if reloadKilled(p.command.ProcessState) != p.forced {
				t.Fatal("forced outcome did not match the actual OS wait state")
			}
		})
	}
}

func TestReloadProcessStopPreservesOutputFailure(t *testing.T) {
	want := errors.New("output unavailable")
	p := reloadTestProcess(t, "cooperate", want)
	if err := p.stop(time.Second); !errors.Is(err, want) {
		t.Fatal("terminal output failure lost", err)
	}
}
