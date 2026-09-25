package management

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// runscript compiles a trusted, standalone Go main file. No project resources
// are opened in this parent process; the script chooses its own bootstrap.
func runScriptCommand() Command {
	return Command{Name: "runscript", Help: "Compile and run a trusted Go script in a separate process", Configure: func(f *flag.FlagSet) Runner {
		timeout := f.Duration("timeout", 5*time.Minute, "total compile/run deadline (positive, at most 24h)")
		maxOutput := f.Int64("max-output", 1<<20, "combined compiler/script stdout and stderr limit in bytes (1..67108864)")
		f.Usage = func() {
			_, _ = fmt.Fprintln(f.Output(), "Usage: manage runscript [options] scripts/file.go -- [script arguments]")
			f.PrintDefaults()
		}
		return func(ctx context.Context, i *Invocation, args []string) error {
			if len(args) == 0 || *timeout <= 0 || *timeout > 24*time.Hour || *maxOutput < 1 || *maxOutput > 64<<20 {
				return &CommandError{2, "runscript requires a Go file, a positive timeout up to 24h, and --max-output between 1 and 67108864", nil}
			}
			return runScript(ctx, i, args, *timeout, *maxOutput)
		}
	}}
}

func runScript(ctx context.Context, i *Invocation, args []string, timeout time.Duration, maxOutput int64) (result error) {
	root, err := filepath.Abs(i.Project.Root)
	if err != nil {
		return &CommandError{1, "runscript cannot resolve the project directory", err}
	}
	if err = validateScriptSource(root, args[0]); err != nil {
		return &CommandError{2, "runscript requires a regular .go file inside the project and a go.mod at the project root", err}
	}
	tool, err := scriptGoTool(runtime.GOROOT())
	if err != nil {
		return &CommandError{1, "runscript requires an installed Go toolchain; use a source-equipped operations image, not a binary-only image", err}
	}
	// The directory is private, outside the source tree, and owned by this call.
	temp, err := os.MkdirTemp("", "gogo-runscript-")
	if err != nil {
		return &CommandError{1, "runscript cannot create its temporary build directory", err}
	}
	defer func() {
		if cleanup := os.RemoveAll(temp); cleanup != nil {
			result = &CommandError{1, "runscript could not remove its temporary executable", errors.Join(result, cleanup)}
		}
	}()
	deadline, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	runCtx, cancel := context.WithCancel(deadline)
	defer cancel()
	output := &scriptOutput{remaining: maxOutput, cancel: cancel}
	stdout := &scriptWriter{output: output, writer: i.Stdout}
	stderr := &scriptWriter{output: output, writer: i.Stderr}
	executable := filepath.Join(temp, "script")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	// Use a real compiler (including generics), not an interpreter or go run's
	// intermediate process. Never silently download a toolchain/dependency.
	build := exec.CommandContext(runCtx, tool, "build", "-mod=readonly", "-trimpath", "-o", executable, "./"+filepath.ToSlash(filepath.Clean(args[0])))
	build.Dir, build.Env = root, scriptBuildEnvironment(os.Environ())
	build.Stdout, build.Stderr = stdout, stderr
	build.WaitDelay = 2 * time.Second
	err = build.Run()
	if failure := scriptFailure(deadline, output, err, true); failure != nil {
		return failure
	}
	child := exec.CommandContext(runCtx, executable, args[1:]...)
	child.Dir = root
	child.Stdin, child.Stdout, child.Stderr = i.Stdin, stdout, stderr
	// Inherit process environment, not serialized settings or secrets passed on
	// argv. gogo.Script loads the child's project .env and configuration itself.
	child.WaitDelay = i.Settings.Duration("GOGO_SHUTDOWN_GRACE")
	if child.WaitDelay <= 0 {
		child.WaitDelay = 2 * time.Second
	}
	child.Cancel = func() error {
		// Give gogo.Script's signal context a chance to close resources. Cmd's
		// WaitDelay forcibly kills the direct child if it does not cooperate.
		if err := child.Process.Signal(os.Interrupt); err != nil {
			return child.Process.Kill()
		}
		return nil
	}
	err = child.Run()
	return scriptFailure(deadline, output, err, false)
}

func validateScriptSource(directory, name string) error {
	if !filepath.IsLocal(name) || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
		return errors.New("invalid script path")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, path := range []string{"go.mod", name} {
		info, err := root.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("source must be a regular file")
		}
	}
	return nil
}

func scriptGoTool(goroot string) (string, error) {
	// A Go launcher on PATH may be older than the local toolchain used to build
	// manage. Prefer that installed toolchain without allowing auto-downloads.
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if tool, err := exec.LookPath(filepath.Join(goroot, "bin", name)); err == nil {
		return tool, nil
	}
	return exec.LookPath(name)
}

func scriptBuildEnvironment(environment []string) []string {
	var result []string
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		// Keep ordinary build settings (workspace, caches, CGO), but don't pass
		// framework credentials to the compiler or permit implicit build hooks.
		if strings.HasPrefix(key, "GOGO_") {
			continue
		}
		switch key {
		case "GOFLAGS", "GOENV", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GONOPROXY", "GOVCS", "GO111MODULE", "GOROOT", "GOOS", "GOARCH":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GOFLAGS=", "GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GONOPROXY=none", "GOVCS=*:off", "GO111MODULE=on", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
}

type scriptOutput struct {
	mu        sync.Mutex
	remaining int64
	cancel    context.CancelFunc
	err       error
}

type scriptWriter struct {
	output *scriptOutput
	writer io.Writer
}

func (w *scriptWriter) Write(p []byte) (int, error) {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	if w.output.err != nil {
		return 0, w.output.err
	}
	limited := int64(len(p)) > w.output.remaining
	if limited {
		p = p[:int(w.output.remaining)]
	}
	n, err := w.writer.Write(p)
	w.output.remaining -= int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err == nil && limited {
		err = errors.New("script output limit exceeded")
	}
	if err != nil {
		w.output.err = err
		w.output.cancel()
	}
	return n, err
}

func scriptFailure(ctx context.Context, output *scriptOutput, err error, compiling bool) error {
	output.mu.Lock()
	outputErr := output.err
	output.mu.Unlock()
	if outputErr != nil {
		return &CommandError{1, "runscript output limit exceeded or output delivery failed", outputErr}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &CommandError{124, "runscript timed out; check effects before retrying", ctx.Err()}
	}
	if ctx.Err() != nil {
		return &CommandError{130, "runscript was canceled; check effects before retrying", ctx.Err()}
	}
	if err == nil {
		return nil
	}
	if compiling {
		return &CommandError{1, "runscript compilation failed; check source, the installed toolchain, and locally cached dependencies", err}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return &CommandError{exit.ExitCode(), "runscript exited unsuccessfully; check effects before retrying", err}
	}
	return &CommandError{1, "runscript could not complete; check effects before retrying", err}
}
