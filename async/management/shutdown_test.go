package management

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownDeadlineDoesNotClaimUncooperativeHandlerStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	exited := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- supervise(ctx, []func(context.Context) error{func(context.Context) error { close(started); <-release; close(exited); return nil }}, 20*time.Millisecond)
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown ignored grace deadline")
	}
	select {
	case <-exited:
		t.Fatal("test handler unexpectedly stopped")
	default:
	}
	close(release)
	<-exited
}
