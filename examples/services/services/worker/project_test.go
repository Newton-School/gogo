package main

import (
	"context"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/management"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWorkerRequiresOnlyRedisAndHasNoHTTPOrMigration(t *testing.T) {
	p := Project()
	if p.Handler != nil || len(p.Apps) != 0 {
		t.Fatal("worker mounted an HTTP app")
	}
	found := false
	for _, command := range p.Commands {
		if command.Name == "migrate" || command.Name == "beat" {
			t.Fatal("unselected role installed")
		}
		if command.Name != "worker" {
			continue
		}
		found = true
		_, err := p.Schema.Load(nil, command.Resources...)
		if err == nil || !strings.Contains(err.Error(), "GOGO_REDIS_URL") || strings.Contains(err.Error(), "DATABASE") || strings.Contains(err.Error(), "SECRET_KEY") {
			t.Fatal(err)
		}
		if _, err := p.Schema.Load(map[string]string{"GOGO_REDIS_URL": "redis://127.0.0.1:6379/0"}, command.Resources...); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("worker command missing")
	}
}

func TestWorkerNativeStartup(t *testing.T) {
	redis := fixture.Start(t)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := Project()
	p.Root = t.TempDir()
	p.Environment = map[string]string{"GOGO_REDIS_URL": redis.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := management.Call(ctx, p, []string{"worker", "--once"}, management.Options{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
}
