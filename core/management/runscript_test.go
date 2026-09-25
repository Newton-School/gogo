package management

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
)

func scriptProject(t *testing.T, source string) Project {
	t.Helper()
	isolateCommandEnvironment(t)
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":         "module example.com/script-test\n\ngo 1.26.8\n",
		"maintenance.go": source,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return Project{Root: root}
}

func scriptCall(t *testing.T, project Project, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, project, append([]string{"manage", "runscript"}, args...), Options{
		Stdin: strings.NewReader("input"), Stdout: &stdout, Stderr: &stderr,
	})
	return code, stdout.String(), stderr.String()
}

func TestRunScriptCompilesRealGoAndForwardsArguments(t *testing.T) {
	p := scriptProject(t, `package main
import("fmt"; "io"; "os")
func first[T any](values []T) T { return values[0] }
func main() {
 data, _ := io.ReadAll(os.Stdin)
 fmt.Printf("%s|%v|%s|%s", first([]string{"generic"}), os.Args[1:], data, os.Getenv("SCRIPT_TEST_VALUE"))
 fmt.Fprint(os.Stderr, "stderr")
}
`)
	t.Setenv("SCRIPT_TEST_VALUE", "inherited")
	opened, ready := false, false
	p.RuntimeResources = []string{"database"} // missing DB is irrelevant in parent
	p.ResourceFactory = func(conf.Values, []string) ([]app.Resource, error) { opened = true; return nil, nil }
	p.Apps = []app.Config{{Name: "test", Label: "test", Ready: func(context.Context, *app.Registry) error { ready = true; return nil }}}
	before, err := os.ReadFile(filepath.Join(p.Root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := scriptCall(t, p, context.Background(), "maintenance.go", "--", "--timeout=123", "two words")
	if code != 0 || stdout != "generic|[--timeout=123 two words]|input|inherited" || stderr != "stderr" || opened || ready {
		t.Fatalf("code=%d stdout=%q stderr=%q opened=%v ready=%v", code, stdout, stderr, opened, ready)
	}
	after, err := os.ReadFile(filepath.Join(p.Root, "go.mod"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("go.mod changed: %v", err)
	}
	entries, err := os.ReadDir(p.Root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected project artifacts: %v %v", entries, err)
	}
}

func TestRunScriptValidationAndHelp(t *testing.T) {
	p := scriptProject(t, "package main\nfunc main() {}\n")
	for _, args := range [][]string{
		nil, {"../maintenance.go"}, {"missing.go"}, {"go.mod"}, {"maintenance_test.go"},
		{filepath.Join(p.Root, "maintenance.go")},
		{"maintenance.go", "--timeout=0"}, {"maintenance.go", "--timeout=25h"},
		{"maintenance.go", "--max-output=0"}, {"maintenance.go", "--max-output=67108865"},
		{"maintenance.go", "--unknown"},
	} {
		code, _, stderr := scriptCall(t, p, context.Background(), args...)
		if code != 2 {
			t.Fatalf("%v: code=%d stderr=%s", args, code, stderr)
		}
	}
	if code, _, stderr := scriptCall(t, p, context.Background(), "--help"); code != 0 || !strings.Contains(stderr, "-- [script arguments]") {
		t.Fatal(code, stderr)
	}
	if err := os.Remove(filepath.Join(p.Root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := scriptCall(t, p, context.Background(), "maintenance.go"); code != 2 {
		t.Fatal(code)
	}
}

func TestRunScriptRejectsNonregularAndEscapingSources(t *testing.T) {
	p := scriptProject(t, "package main\nfunc main() {}\n")
	if err := os.Mkdir(filepath.Join(p.Root, "directory.go"), 0700); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := scriptCall(t, p, context.Background(), "directory.go"); code != 2 {
		t.Fatal(code)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.Root, "link.go")); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	if code, _, _ := scriptCall(t, p, context.Background(), "link.go"); code != 2 {
		t.Fatal(code)
	}
}

func TestRunScriptCompileFailureNeverRuns(t *testing.T) {
	p := scriptProject(t, "package main\nfunc main() { invalid code }\n")
	code, stdout, stderr := scriptCall(t, p, context.Background(), "maintenance.go")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "compilation failed") {
		t.Fatal(code, stdout, stderr)
	}
}

func TestRunScriptMissingToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if tool, err := scriptGoTool(t.TempDir()); err == nil {
		t.Fatalf("unexpected toolchain: %s", tool)
	}
}

func TestRunScriptPreservesExitCode(t *testing.T) {
	p := scriptProject(t, "package main\nimport \"os\"\nfunc main() { os.Exit(7) }\n")
	code, _, stderr := scriptCall(t, p, context.Background(), "maintenance.go")
	if code != 7 || !strings.Contains(stderr, "exited unsuccessfully") {
		t.Fatal(code, stderr)
	}
}

func TestRunScriptBoundsOutputAndCleansTemporaryExecutable(t *testing.T) {
	p := scriptProject(t, "package main\nimport (\"fmt\"; \"os\"; \"strings\")\nfunc main() { fmt.Println(os.Args[0]); fmt.Print(strings.Repeat(\"x\", 1024)) }\n")
	code, stdout, stderr := scriptCall(t, p, context.Background(), "maintenance.go", "--max-output=512")
	if code != 1 || len(stdout) != 512 || !strings.Contains(stderr, "output limit") {
		t.Fatal(code, len(stdout), stderr)
	}
	executable, _, ok := strings.Cut(stdout, "\n")
	if !ok {
		t.Fatal("missing executable path")
	}
	if _, err := os.Stat(filepath.Dir(executable)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}

func TestRunScriptCancellation(t *testing.T) {
	p := scriptProject(t, "package main\nfunc main() {}\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code, _, stderr := scriptCall(t, p, ctx, "maintenance.go"); code != 130 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := scriptCall(t, p, context.Background(), "maintenance.go", "--timeout=1ns"); code != 124 {
		t.Fatal(code, stderr)
	}
}

func TestRunScriptCancelsRunningChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("interrupt handling is Unix-specific")
	}
	p := scriptProject(t, `package main
import("context"; "fmt"; "os"; "os/signal")
func main() {
 ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
 defer stop()
 fmt.Println("ready")
 <-ctx.Done()
 fmt.Println("closed")
}
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	writer := &scriptCancelWriter{buffer: &stdout, cancel: cancel}
	code := Run(ctx, p, []string{"manage", "runscript", "maintenance.go"}, Options{Stdin: strings.NewReader(""), Stdout: writer, Stderr: &stderr})
	if code != 130 || !strings.Contains(stdout.String(), "closed") {
		t.Fatal(code, stdout.String(), stderr.String())
	}
}

func TestRunScriptKillsUncooperativeChildAfterGrace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("interrupt handling is Unix-specific")
	}
	p := scriptProject(t, `package main
import("fmt"; "os"; "os/signal"; "time")
func main() {
 signal.Ignore(os.Interrupt)
 fmt.Println("ready")
 time.Sleep(time.Hour)
}
`)
	p.Environment = map[string]string{"GOGO_SHUTDOWN_GRACE": "50ms"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	writer := &scriptCancelWriter{buffer: &stdout, cancel: cancel}
	start := time.Now()
	code := Run(ctx, p, []string{"manage", "runscript", "maintenance.go"}, Options{Stdin: strings.NewReader(""), Stdout: writer, Stderr: &stderr})
	if code != 130 || stdout.String() != "ready\n" || time.Since(start) > 10*time.Second {
		t.Fatal(code, stdout.String(), stderr.String(), time.Since(start))
	}
}

type scriptCancelWriter struct {
	buffer *bytes.Buffer
	cancel context.CancelFunc
}

func (w *scriptCancelWriter) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	if strings.Contains(w.buffer.String(), "ready") {
		w.cancel()
	}
	return n, err
}

func TestRunScriptBuildEnvironment(t *testing.T) {
	env := scriptBuildEnvironment([]string{"PATH=/tools", "GOGO_SECRET_KEY=private", "GOFLAGS=-toolexec=hook", "GOTOOLCHAIN=auto", "GOPROXY=direct", "GONOPROXY=*", "GOROOT=/wrong", "GOWORK=/project/go.work", "CGO_ENABLED=0"})
	values := conf.Environment(env)
	if _, found := values["GOGO_SECRET_KEY"]; found {
		t.Fatal("compiler inherited framework credentials")
	}
	for key, want := range map[string]string{"GOFLAGS": "", "GOENV": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GONOPROXY": "none", "GOVCS": "*:off", "GO111MODULE": "on", "GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH, "PATH": "/tools", "GOWORK": "/project/go.work", "CGO_ENABLED": "0"} {
		if values[key] != want {
			t.Fatalf("%s = %q, want %q", key, values[key], want)
		}
	}
	if _, found := values["GOROOT"]; found {
		t.Fatal("compiler inherited unrelated GOROOT")
	}
}

func TestScriptOutputSharedBudgetAndWriterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &scriptOutput{remaining: 3, cancel: cancel}
	var first, second bytes.Buffer
	a := &scriptWriter{output: output, writer: &first}
	b := &scriptWriter{output: output, writer: &second}
	if n, err := a.Write([]byte("ab")); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := b.Write([]byte("cd")); n != 1 || err == nil {
		t.Fatal(n, err)
	}
	if first.String()+second.String() != "abc" || ctx.Err() == nil {
		t.Fatal(first.String(), second.String(), ctx.Err())
	}
	if n, err := a.Write([]byte("e")); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	failed := &scriptOutput{remaining: 3, cancel: cancel}
	w := &scriptWriter{output: failed, writer: scriptShortWriter{}}
	if _, err := w.Write([]byte("ab")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}

type scriptShortWriter struct{}

func (scriptShortWriter) Write([]byte) (int, error) { return 0, nil }

func TestRunScriptHelpIsCoreBuiltin(t *testing.T) {
	isolateCommandEnvironment(t)
	var output bytes.Buffer
	if err := Call(context.Background(), Project{Root: t.TempDir()}, []string{"help"}, Options{Stdout: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "runscript") {
		t.Fatal(output.String())
	}
	// No registration or optional module is needed.
	command := runScriptCommand()
	if command.OpenResources || !reflect.DeepEqual(command.Resources, []string(nil)) {
		t.Fatal(command)
	}
}

func TestRunScriptDeadlineAppliesToChild(t *testing.T) {
	p := scriptProject(t, "package main\nimport \"time\"\nfunc main() { time.Sleep(time.Hour) }\n")
	start := time.Now()
	code, _, stderr := scriptCall(t, p, context.Background(), "maintenance.go", "--timeout=3s")
	if code != 124 || time.Since(start) > 10*time.Second {
		t.Fatal(code, time.Since(start), stderr)
	}
}
