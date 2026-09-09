//go:build darwin || linux

package management

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/conf"
)

// These tests exercise the real supervisor and its owned process/Wait boundary.
// The compiler and candidate are the current compiled test binary, reached by
// exec-only shims: no extra compiler, descendant process, or HTTP listener is
// needed. The integration consumer separately proves real Go builds and HTTP.
func TestReloadSupervisorHelper(t *testing.T) {
	control := os.Getenv("GOGO_TEST_SUPERVISOR_CONTROL")
	if control == "" {
		return
	}
	var arguments []string
	for index, value := range os.Args {
		if value == "--" {
			arguments = os.Args[index+1:]
			break
		}
	}
	code, err := runReloadSupervisorHelper(control, arguments)
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "reload supervisor fixture failed\n")
		code = 90
	}
	// In particular, diffsettings must not acquire the test runner's PASS text.
	os.Exit(code)
}

func runReloadSupervisorHelper(control string, arguments []string) (int, error) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if len(arguments) == 6 && arguments[0] == "compiler" && arguments[1] == "build" && arguments[2] == "-trimpath" && arguments[3] == "-o" && arguments[5] == "manage.go" {
		output := arguments[4]
		sequence, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(output), "manage-"))
		if err != nil || sequence < 1 {
			return 0, errors.New("invalid fixture sequence")
		}
		source, err := os.ReadFile("manage.go")
		if err != nil {
			return 0, err
		}
		value, _, ok := strings.Cut(strings.TrimPrefix(string(source), "package main\n\nconst generation = "), "\n")
		generation, err := strconv.Atoi(value)
		if !ok || err != nil || generation < 1 {
			return 0, errors.New("invalid fixture generation")
		}
		name := fmt.Sprintf("build-%d", sequence)
		if err = reloadSupervisorEvent(control, name+"-started", strconv.Itoa(generation)); err != nil {
			return 0, err
		}
		if _, err = os.Stat(filepath.Join(control, "hold-"+name)); err == nil {
			interrupted, e := reloadSupervisorGate(control, "release-"+name, signals)
			if e != nil {
				return 0, e
			}
			if interrupted {
				return 0, reloadSupervisorEvent(control, name+"-exited", "interrupted")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		// Both interpolated values have been parsed as positive integers. The
		// executable path is quoted environment data, never shell source.
		script := fmt.Sprintf("#!/bin/sh\nexec \"$GOGO_TEST_SUPERVISOR_BINARY\" -test.run='^TestReloadSupervisorHelper$' -- candidate %d %d \"$@\"\n", sequence, generation)
		if err = os.WriteFile(output, []byte(script), 0700); err != nil {
			return 0, err
		}
		return 0, reloadSupervisorEvent(control, name+"-exited", "normal")
	}
	if len(arguments) < 4 || arguments[0] != "candidate" {
		return 0, errors.New("invalid fixture arguments")
	}
	sequence, err := strconv.Atoi(arguments[1])
	if err != nil || sequence < 1 {
		return 0, errors.New("invalid fixture candidate")
	}
	generation, err := strconv.Atoi(arguments[2])
	if err != nil || generation < 1 {
		return 0, errors.New("invalid fixture generation")
	}
	name := fmt.Sprintf("child-%d", sequence)
	switch arguments[3] {
	case "check":
		if len(arguments) != 4 {
			return 0, errors.New("unexpected check arguments")
		}
		return 0, reloadSupervisorEvent(control, fmt.Sprintf("check-%d", sequence), "checked")
	case "diffsettings":
		if len(arguments) != 4 {
			return 0, errors.New("unexpected settings arguments")
		}
		if err = reloadSupervisorEvent(control, fmt.Sprintf("settings-%d", sequence), "validated"); err != nil {
			return 0, err
		}
		_, err = io.WriteString(os.Stdout, "{\"GOGO_ENV\":\"test\",\"GOGO_SHUTDOWN_GRACE\":1000000000}\n")
		return 0, err
	case "runserver":
		if len(arguments) != 6 || arguments[4] != "--addr" || arguments[5] != "127.0.0.1:0" {
			return 0, errors.New("unexpected server arguments")
		}
		if err = reloadSupervisorEvent(control, name+"-started", strconv.Itoa(generation)); err != nil {
			return 0, err
		}
		select {
		case <-signals:
		case <-time.After(20 * time.Second):
			return 0, errors.New("fixture server was not stopped")
		}
		if err = reloadSupervisorEvent(control, name+"-draining", "draining"); err != nil {
			return 0, err
		}
		mode, e := os.ReadFile(filepath.Join(control, "shutdown-mode"))
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return 0, e
		}
		switch string(mode) {
		case "hold":
			if _, err = reloadSupervisorGate(control, fmt.Sprintf("release-drain-%d", sequence), signals); err != nil {
				return 0, err
			}
		case "reject":
			return 7, reloadSupervisorEvent(control, name+"-exited", "rejected")
		case "", "normal":
		default:
			return 0, errors.New("invalid fixture shutdown mode")
		}
		return 0, reloadSupervisorEvent(control, name+"-exited", "normal")
	default:
		return 0, errors.New("unexpected fixture command")
	}
}

func reloadSupervisorEvent(control, name, value string) error {
	path := filepath.Join(control, name)
	if err := os.WriteFile(path+".pending", []byte(value), 0600); err != nil {
		return err
	}
	return os.Rename(path+".pending", path)
}

func reloadSupervisorGate(control, name string, signals <-chan os.Signal) (bool, error) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(filepath.Join(control, name)); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		select {
		case <-signals:
			return true, nil
		case <-deadline.C:
			return false, errors.New("fixture gate was not released")
		case <-ticker.C:
		}
	}
}

type reloadSupervisorOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *reloadSupervisorOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(value) > (1<<20)-w.buffer.Len() {
		return 0, errors.New("supervisor fixture output exceeded limit")
	}
	return w.buffer.Write(value)
}

func (w *reloadSupervisorOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

type reloadSupervisorFixture struct {
	t        *testing.T
	project  string
	control  string
	entry    runServerEntry
	settings conf.Values
	output   reloadSupervisorOutput
	cancel   context.CancelFunc
	done     chan struct{}
	err      error // Written before closing done; read only after done is closed.
}

func newReloadSupervisorFixture(t *testing.T) *reloadSupervisorFixture {
	t.Helper()
	parent := t.TempDir()
	f := &reloadSupervisorFixture{t: t, project: filepath.Join(parent, "project"), control: filepath.Join(parent, "control")}
	tools := filepath.Join(parent, "tools")
	for _, directory := range []string{filepath.Join(f.project, "apps"), f.control, tools} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\nexec \"$GOGO_TEST_SUPERVISOR_BINARY\" -test.run='^TestReloadSupervisorHelper$' -- compiler \"$@\"\n"
	if err = os.WriteFile(filepath.Join(tools, "go"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PATH=") && !strings.HasPrefix(value, "GORACE=") && !strings.HasPrefix(value, "GOGO_TEST_SUPERVISOR_") && !strings.HasPrefix(value, "GOGO_TEST_RELOAD_PROCESS=") {
			f.entry.environment = append(f.entry.environment, value)
		}
	}
	// Race detection remains enabled; only its exit sleep is excluded from the
	// fixture's deliberately bounded cooperative process shutdown.
	f.entry.environment = append(f.entry.environment, "PATH="+tools, "GORACE=atexit_sleep_ms=0", "GOGO_TEST_SUPERVISOR_BINARY="+executable, "GOGO_TEST_SUPERVISOR_CONTROL="+f.control)
	f.settings, err = conf.CoreSchema().Load(map[string]string{"GOGO_ENV": "test", "GOGO_SHUTDOWN_GRACE": "1s"})
	if err != nil {
		t.Fatal(err)
	}
	f.source(1)
	return f
}

func (f *reloadSupervisorFixture) source(generation int) {
	f.t.Helper()
	name := filepath.Join(f.project, "manage.go")
	value := fmt.Sprintf("package main\n\nconst generation = %d\n\nfunc main() {}\n", generation)
	if err := os.WriteFile(filepath.Join(f.project, ".next"), []byte(value), 0600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.project, ".next"), name); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reloadSupervisorFixture) set(name, value string) {
	f.t.Helper()
	if err := reloadSupervisorEvent(f.control, name, value); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reloadSupervisorFixture) start() {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel, f.done = cancel, make(chan struct{})
	f.t.Cleanup(func() {
		cancel()
		select {
		case <-f.done:
		case <-time.After(10 * time.Second):
			f.t.Error("owned supervisor cleanup did not finish")
		}
	})
	go func() {
		i := Invocation{Project: &Project{Root: f.project}, Settings: f.settings, Stdout: &f.output, Stderr: &f.output}
		f.err = runReload(ctx, i, RunServerOptions{Reload: true, Address: "127.0.0.1:0"}, &f.entry)
		close(f.done)
	}()
}

func (f *reloadSupervisorFixture) event(name string) string {
	f.t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		value, err := os.ReadFile(filepath.Join(f.control, name))
		if err == nil {
			return string(value)
		}
		if !errors.Is(err, os.ErrNotExist) {
			f.t.Fatal(err)
		}
		select {
		case <-f.done:
			// A terminal helper may publish its event between the read above
			// and the supervisor's observed Wait completion.
			if value, err = os.ReadFile(filepath.Join(f.control, name)); err == nil {
				return string(value)
			}
			f.t.Fatalf("supervisor stopped before %s: %v; %s", name, f.err, f.output.String())
		case <-deadline.C:
			f.t.Fatalf("no %s witness; %s", name, f.output.String())
		case <-ticker.C:
		}
	}
}

func (f *reloadSupervisorFixture) absent(name string) {
	f.t.Helper()
	if _, err := os.Stat(filepath.Join(f.control, name)); !errors.Is(err, os.ErrNotExist) {
		f.t.Fatalf("unexpected %s witness: %v", name, err)
	}
}

func (f *reloadSupervisorFixture) finish() error {
	f.t.Helper()
	select {
	case <-f.done:
		return f.err
	case <-time.After(10 * time.Second):
		f.t.Fatalf("supervisor did not finish; %s", f.output.String())
		return nil
	}
}

func TestReloadSupervisorDiscardsObsoleteBuildAndCoalescesChanges(t *testing.T) {
	f := newReloadSupervisorFixture(t)
	f.start()
	if got := f.event("child-1-started"); got != "1" {
		t.Fatal("wrong initial generation", got)
	}
	f.set("hold-build-2", "hold")
	f.source(2)
	if got := f.event("build-2-started"); got != "2" {
		t.Fatal("second build did not capture the second generation", got)
	}
	f.absent("child-1-draining")
	f.source(3)
	f.source(4)
	f.set("release-build-2", "release")
	if got := f.event("build-3-started"); got != "4" {
		t.Fatal("coalesced build did not use the latest generation", got)
	}
	if got := f.event("child-3-started"); got != "4" {
		t.Fatal("replacement did not use the latest generation", got)
	}
	f.absent("child-2-started")
	if got := f.event("child-1-exited"); got != "normal" {
		t.Fatal("replacement started without cooperative old-child exit", got)
	}
	f.cancel()
	if err := f.finish(); !errors.Is(err, context.Canceled) {
		t.Fatal("terminal cancellation was lost", err)
	}
}

func TestReloadSupervisorCancellationDuringBuild(t *testing.T) {
	f := newReloadSupervisorFixture(t)
	f.set("hold-build-1", "hold")
	f.start()
	f.event("build-1-started")
	f.cancel()
	if err := f.finish(); !errors.Is(err, context.Canceled) {
		t.Fatal("build cancellation was lost", err)
	}
	if got := f.event("build-1-exited"); got != "interrupted" {
		t.Fatal("owned build did not observe cancellation", got)
	}
	f.absent("check-1")
	f.absent("child-1-started")
}

func TestReloadSupervisorCancellationDuringOldChildDrain(t *testing.T) {
	f := newReloadSupervisorFixture(t)
	f.start()
	f.event("child-1-started")
	f.set("shutdown-mode", "hold")
	f.source(2)
	f.event("child-1-draining")
	f.event("settings-2")
	f.cancel()
	f.set("release-drain-1", "release")
	if err := f.finish(); !errors.Is(err, context.Canceled) {
		t.Fatal("drain cancellation was lost", err)
	}
	if got := f.event("child-1-exited"); got != "normal" {
		t.Fatal("old child did not finish its observed drain", got)
	}
	f.absent("child-2-started")
}

func TestReloadSupervisorRefusesReplacementAfterShutdownFailure(t *testing.T) {
	f := newReloadSupervisorFixture(t)
	f.start()
	f.event("child-1-started")
	f.set("shutdown-mode", "reject")
	f.source(2)
	err := f.finish()
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("old-child shutdown failure did not stop replacement", err)
	}
	f.event("check-2")
	f.event("settings-2")
	if got := f.event("child-1-exited"); got != "rejected" {
		t.Fatal("missing actual failed old-child exit", got)
	}
	f.absent("child-2-started")
	f.absent("build-3-started")
}
