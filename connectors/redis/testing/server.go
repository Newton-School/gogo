// Package testing starts isolated Redis instances for adapter conformance tests.
package testing

import (
	"context"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"

	connector "github.com/Newton-School/gogo/connectors/redis"
)

func Start(t testing.TB) connector.Config {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for real adapter conformance")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	cmd := exec.Command(binary, "--bind", "127.0.0.1", "--port", strconv.Itoa(port), "--dir", t.TempDir(), "--save", "", "--appendonly", "no", "--maxmemory-policy", "noeviction", "--loglevel", "warning")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned Redis test process did not exit")
		}
	})
	cfg := connector.Config{URL: "redis://127.0.0.1:" + strconv.Itoa(port), Namespace: "test", Role: connector.CacheRole, Development: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		connection, err := connector.Open(ctx, cfg)
		if err == nil {
			_ = connection.Close()
			return cfg
		}
		select {
		case <-ctx.Done():
			t.Fatal("Redis test startup timed out")
			return cfg
		case err := <-done:
			t.Fatalf("Redis test process exited: %v", err)
			return cfg
		case <-time.After(10 * time.Millisecond):
		}
	}
}
