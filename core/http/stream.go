package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Event struct {
	ID, Name, Data string
	Retry          time.Duration
}
type SSEConfig struct {
	Heartbeat, IdleTimeout, WriteTimeout time.Duration
	MaxEventBytes                        int
}

// SSE pulls one event at a time from an application-owned bounded channel. The
// producer must select on context cancellation; this function creates no producer goroutine.
func SSE(w http.ResponseWriter, r *http.Request, events <-chan Event, config SSEConfig) error {
	if config.Heartbeat == 0 {
		config.Heartbeat = 15 * time.Second
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 5 * time.Minute
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.MaxEventBytes == 0 {
		config.MaxEventBytes = 1 << 20
	}
	if config.Heartbeat < 0 || config.IdleTimeout < 0 || config.WriteTimeout < 0 || config.MaxEventBytes < 1 {
		return errors.New("invalid SSE limits")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	ticker := time.NewTicker(config.Heartbeat)
	defer ticker.Stop()
	idle := time.NewTimer(config.IdleTimeout)
	defer idle.Stop()
	write := func(value string) error {
		if err := controller.SetWriteDeadline(time.Now().Add(config.WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := io.WriteString(w, value); err != nil {
			return err
		}
		return controller.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-idle.C:
			return context.DeadlineExceeded
		case <-ticker.C:
			if err := write(": heartbeat\n\n"); err != nil {
				return err
			}
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if len(event.Data) > config.MaxEventBytes || strings.ContainsAny(event.ID+event.Name, "\r\n\x00") {
				return errors.New("invalid SSE event")
			}
			var b strings.Builder
			if event.ID != "" {
				fmt.Fprintf(&b, "id: %s\n", event.ID)
			}
			if event.Name != "" {
				fmt.Fprintf(&b, "event: %s\n", event.Name)
			}
			if event.Retry > 0 {
				fmt.Fprintf(&b, "retry: %d\n", event.Retry.Milliseconds())
			}
			for _, line := range strings.Split(strings.ReplaceAll(strings.ReplaceAll(event.Data, "\r\n", "\n"), "\r", "\n"), "\n") {
				fmt.Fprintf(&b, "data: %s\n", line)
			}
			b.WriteByte('\n')
			if err := write(b.String()); err != nil {
				return err
			}
			idle.Reset(config.IdleTimeout)
		}
	}
}
