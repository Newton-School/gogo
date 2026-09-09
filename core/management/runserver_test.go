package management

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
)

func TestRunServerAddressUsesConcreteLegacyDefault(t *testing.T) {
	settings, err := (conf.Schema{{Name: "GOGO_CUSTOM"}}).Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := runServerAddress("", settings); got != "127.0.0.1:8000" {
		t.Fatal("empty settings could bind a wildcard", got)
	}
	if got := runServerAddress("127.0.0.1:9876", settings); got != "127.0.0.1:9876" {
		t.Fatal(got)
	}
}

func TestRunServerRejectsReloadBeforeOpeningResources(t *testing.T) {
	isolateCommandEnvironment(t)
	dir := t.TempDir()
	var effects atomic.Int32
	p := Project{Root: dir, Environment: map[string]string{"GOGO_ENV": "production"}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) { effects.Add(1); return nil, nil }, Handler: func(*app.Registry, conf.Values) (http.Handler, error) {
		effects.Add(1)
		return http.NotFoundHandler(), nil
	}, Apps: []app.Config{{Name: "demo", Label: "demo", Ready: func(context.Context, *app.Registry) error { effects.Add(1); return nil }}}}
	for _, args := range [][]string{{"runserver", "--reload"}, {"serve", "--reload"}, {"runserver", "--watch", "assets"}, {"runserver", "--reload", "--watch", "../outside"}, {"runserver", "--reload", "unexpected"}} {
		if err := Call(context.Background(), p, args, Options{Stdout: io.Discard, Stderr: io.Discard}); err == nil {
			t.Fatalf("invalid invocation accepted: %v", args)
		}
	}
	if effects.Load() != 0 {
		t.Fatal("reload parent or refused flags opened runtime", effects.Load())
	}
}

func TestRunServerCapturesProjectBeforeRegistration(t *testing.T) {
	isolateCommandEnvironment(t)
	t.Setenv("GOGO_ENV", "test")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	environment := map[string]string{"GOGO_ENV": "test", "GOGO_HTTP_ADDR": "127.0.0.1:0", "GOGO_SHUTDOWN_GRACE": "1s"}
	var effects atomic.Int32
	p := Project{Root: dir, Environment: environment, Handler: func(_ *app.Registry, settings conf.Values) (http.Handler, error) {
		if settings.String("GOGO_ENV") != "test" {
			t.Error("registration retargeted frozen environment")
		}
		effects.Add(1)
		cancel()
		return http.NotFoundHandler(), nil
	}}
	p.Apps = []app.Config{{Name: "demo", Label: "demo", Register: func(*app.Registry) error {
		environment["GOGO_ENV"] = "production"
		if err := os.Setenv("GOGO_ENV", "production"); err != nil {
			return err
		}
		p.Handler = func(*app.Registry, conf.Values) (http.Handler, error) {
			t.Error("retargeted handler invoked")
			return nil, errors.New("unexpected")
		}
		return nil
	}}}
	err := Call(ctx, p, []string{"runserver"}, Options{Stdout: io.Discard, Stderr: io.Discard})
	if !errors.Is(err, context.Canceled) || effects.Load() != 1 {
		t.Fatal("frozen handler/cancellation lost", effects.Load(), err)
	}
}

type runServerLineWriter struct{ lines chan string }

func (w runServerLineWriter) Write(p []byte) (int, error) {
	select {
	case w.lines <- string(p):
	default:
	}
	return len(p), nil
}

func TestRunServerPlainOwnsOneRuntimeAndRealListener(t *testing.T) {
	isolateCommandEnvironment(t)
	var opened, ready, closed atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(chan string, 4)
	p := Project{Root: t.TempDir(), Environment: map[string]string{"GOGO_HTTP_ADDR": "127.0.0.1:0", "GOGO_SHUTDOWN_GRACE": "1s"}, ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) {
		return []app.Resource{{Name: "owned", Open: func(context.Context) (func(context.Context) error, error) {
			opened.Add(1)
			return func(context.Context) error { closed.Add(1); return nil }, nil
		}}}, nil
	}, Apps: []app.Config{{Name: "demo", Label: "demo", Ready: func(context.Context, *app.Registry) error { ready.Add(1); return nil }}}, Handler: func(*app.Registry, conf.Values) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ready") }), nil
	}}
	done := make(chan error, 1)
	go func() {
		done <- Call(ctx, p, []string{"runserver"}, Options{Stdout: runServerLineWriter{lines}, Stderr: io.Discard})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("plain server did not close")
		}
	})
	var line string
	select {
	case line = <-lines:
	case err := <-done:
		t.Fatal("server exited before listening", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not listen")
	}
	const prefix = "Development server listening at "
	if !strings.HasPrefix(line, prefix) {
		t.Fatal("unexpected readiness diagnostic", line)
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + strings.TrimSpace(strings.TrimPrefix(line, prefix)))
	if err != nil {
		t.Fatal("announced listener was unavailable", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "ready" {
		t.Fatal("unexpected application response", string(body), err)
	}
	cancel()
	select {
	case err = <-done:
		done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("server did not drain")
	}
	if err != nil || opened.Load() != 1 || ready.Load() != 1 || closed.Load() != 1 {
		t.Fatal("runtime ownership mismatch", opened.Load(), ready.Load(), closed.Load(), err)
	}
}

func TestRunServerEnvironmentBoundAndSymlinkRefusal(t *testing.T) {
	isolateCommandEnvironment(t)
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "environment")
	if err := os.WriteFile(outside, []byte("GOGO_ENV=test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".env")); err != nil {
		t.Skip("symlink unavailable")
	}
	p := Project{Root: dir}
	if err := Call(context.Background(), p, []string{"runserver"}, Options{Stdout: io.Discard, Stderr: io.Discard}); err == nil {
		t.Fatal("environment symlink accepted")
	}
	if err := os.Remove(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	reloadTestFile(t, dir, ".env", strings.Repeat("# padding\n", (1<<20)/9+1))
	if err := Call(context.Background(), p, []string{"runserver"}, Options{Stdout: io.Discard, Stderr: io.Discard}); err == nil {
		t.Fatal("oversized total environment accepted")
	}
}

type runServerFaultWriter struct{ panic bool }

func (w runServerFaultWriter) Write([]byte) (int, error) {
	if w.panic {
		panic("private writer detail")
	}
	return 0, nil
}

func TestReloadOutputSerializationAndSafeFailures(t *testing.T) {
	var lock sync.Mutex
	var output bytes.Buffer
	writer := reloadOutput{&lock, &output}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 100 {
				if err := reloadMessage(writer, "line"); err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
	if bytes.Count(output.Bytes(), []byte("line\n")) != 800 {
		t.Fatal("concurrent output was lost")
	}
	for _, panicValue := range []bool{false, true} {
		_, err := (reloadOutput{&lock, runServerFaultWriter{panicValue}}).Write([]byte("test"))
		if err == nil {
			t.Fatal("writer failure ignored")
		}
		public := serverFailure(err)
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(format, public), "private") {
				t.Fatal("public error leaked writer details")
			}
		}
	}
}

func TestRunServerCLIOnlyTrustsOwnedExitMetadata(t *testing.T) {
	isolateCommandEnvironment(t)
	for _, stage := range []string{"register", "freeze", "factory", "ready", "handler", "close"} {
		for _, forgedCode := range []int{0, 2, 17} {
			t.Run(fmt.Sprintf("%s/%d", stage, forgedCode), func(t *testing.T) {
				const secret = "private callback diagnostic"
				forged := &CommandError{Code: forgedCode, Message: secret}
				project := Project{Root: t.TempDir(), Environment: map[string]string{"GOGO_HTTP_ADDR": "127.0.0.1:0", "GOGO_SHUTDOWN_GRACE": "1s"}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				project.Apps = []app.Config{{Name: "demo", Label: "demo", Register: func(*app.Registry) error {
					if stage == "register" {
						return forged
					}
					return nil
				}, Ready: func(context.Context, *app.Registry) error {
					if stage == "ready" {
						return forged
					}
					return nil
				}}}
				project.Freeze = func(*app.Registry) error {
					if stage == "freeze" {
						return forged
					}
					return nil
				}
				project.ResourceFactory = func(conf.Values, []string) ([]app.Resource, error) {
					if stage == "factory" {
						return nil, forged
					}
					return []app.Resource{{Name: "owned", Open: func(context.Context) (func(context.Context) error, error) {
						return func(context.Context) error {
							if stage == "close" {
								return forged
							}
							return nil
						}, nil
					}}}, nil
				}
				project.Handler = func(*app.Registry, conf.Values) (http.Handler, error) {
					if stage == "handler" {
						return nil, forged
					}
					cancel()
					return http.NotFoundHandler(), nil
				}
				var output bytes.Buffer
				code := Run(ctx, project, []string{"manage", "runserver"}, Options{Stdout: &output, Stderr: &output})
				if code != 1 || strings.Contains(output.String(), secret) {
					t.Fatal("untrusted callback chose CLI outcome", code, output.String())
				}
			})
		}
	}
	var output bytes.Buffer
	if code := Run(context.Background(), Project{Root: t.TempDir()}, []string{"manage", "runserver", "--unknown"}, Options{Stdout: &output, Stderr: &output}); code != 2 {
		t.Fatal("owned parse failure no longer has exit2", code)
	}
}
