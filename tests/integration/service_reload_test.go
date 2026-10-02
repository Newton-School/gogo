//go:build darwin || linux

package integration_test

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGeneratedServiceBuildAndReloadStayIndependent(t *testing.T) {
	h := newReloadHarness(t)
	h.run(h.root, h.manager, "startservice", "sessions")
	goBinary := filepath.Join(runtime.GOROOT(), "bin/go")
	h.run(h.root, goBinary, "test", "./services/sessions/...")
	deps := h.run(h.root, goBinary, "list", "-deps", "./services/sessions")
	for _, unwanted := range []string{"example.com/reload-client/config", "example.com/reload-client/apps/", "github.com/Newton-School/gogo/admin", "github.com/Newton-School/gogo/async", "github.com/Newton-School/gogo/connectors/"} {
		if strings.Contains(deps, unwanted) {
			t.Fatalf("service imports unrelated dependency %s", unwanted)
		}
	}
	binary := filepath.Join(t.TempDir(), "sessions")
	h.run(h.root, goBinary, "build", "-o", binary, "./services/sessions")
	h.run(h.root, binary, "build")
	binary = filepath.Join(h.root, "bin/sessions")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	process := startReloadProcess(t, h.root, h.environment, binary, "runserver", "--reload", "--addr="+address)
	waitHealth := func(want string) {
		h.wait("service health "+want, func() bool {
			status, body, err := h.response(address, "/health/live/")
			return err == nil && status == 200 && strings.Contains(string(body), want)
		})
	}
	waitHealth(`"ok"`)
	// The main backend's different router and Ready hooks must never run.
	if status, _, err := h.response(address, "/"); err != nil || status != 404 {
		t.Fatalf("main backend mounted: %d %v", status, err)
	}
	data, err := os.ReadFile(filepath.Join(h.root, "services/sessions/urls.go"))
	if err != nil {
		t.Fatal(err)
	}
	h.write("services/sessions/urls.go", strings.Replace(string(data), `"ok"`, `"reloaded"`, 1))
	waitHealth(`"reloaded"`)
	process.stop(t, true)
}
