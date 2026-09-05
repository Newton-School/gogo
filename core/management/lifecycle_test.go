package management

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/app"
)

func TestPanickingCommandStillCleansResources(t *testing.T) {
	cleaned := false
	a, err := app.Bootstrap(context.Background(), nil, []app.Resource{{Name: "test", Open: func(context.Context) (func(context.Context) error, error) {
		return func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Error("custom settings without grace provided an expired cleanup context")
			}
			cleaned = true
			return nil
		}, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = runAndClose(context.Background(), a, 0, func() error { panic("secret") })
	if err == nil || strings.Contains(err.Error(), "secret") || !cleaned {
		t.Fatalf("panic skipped or disclosed cleanup: %v cleaned=%v", err, cleaned)
	}
}

func TestCommandCleanupBoundsNonCooperativeResource(t *testing.T) {
	release, entered := make(chan struct{}), make(chan struct{})
	a, err := app.Bootstrap(context.Background(), nil, []app.Resource{{Name: "test", Open: func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { close(entered); <-release; return nil }, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = runAndClose(context.Background(), a, 20*time.Millisecond, func() error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded shutdown failure: %v", err)
	}
	<-entered
	close(release)
}
