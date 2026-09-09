package observability

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// SlogExporter emits fixed event keys through an explicitly configured handler.
// The handler/writer is trusted code and must honor context and buffer limits.
// It may add its own fields; this package never adds arbitrary caller attributes.
type SlogExporter struct{ handler slog.Handler }

func NewSlogExporter(handler slog.Handler) (*SlogExporter, error) {
	if nilInterface(handler) {
		return nil, ErrInvalidConfig
	}
	return &SlogExporter{handler: handler}, nil
}

// NewConsoleExporter creates a JSON slog handler without inherited logger fields.
// Flush/Close do not close the caller-owned writer. Writers without context-aware
// I/O remain cooperative: a blocked Write can retain the pipeline's single worker.
func NewConsoleExporter(writer io.Writer, level slog.Level) (*SlogExporter, error) {
	if nilInterface(writer) || (level != slog.LevelDebug && level != slog.LevelInfo && level != slog.LevelWarn && level != slog.LevelError) {
		return nil, ErrInvalidConfig
	}
	return NewSlogExporter(slog.NewJSONHandler(checkedWriter{writer: writer}, &slog.HandlerOptions{Level: level}))
}

// slog's handler observes Write errors but does not check the byte count. Keep a
// short or invalid nil-error Write from becoming a successful export receipt.
type checkedWriter struct{ writer io.Writer }

func (w checkedWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if n < 0 || n > len(data) {
		return 0, io.ErrShortWrite
	}
	if n != len(data) && err == nil {
		return n, io.ErrShortWrite
	}
	return n, err
}

func (s *SlogExporter) Export(ctx context.Context, event Event) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrExporter
		}
	}()
	if s == nil || nilInterface(s.handler) {
		return ErrExporter
	}
	if _, err := inspectContext(ctx); err != nil {
		return err
	}
	if event.Operation() == "" {
		return ErrInvalidEvent
	}
	if !s.handler.Enabled(ctx, slog.LevelInfo) {
		return nil
	}
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "gogo.operation", 0)
	record.AddAttrs(slog.String("operation", event.Operation()), slog.String("route", event.Route()), slog.String("status", event.Status().String()), slog.Duration("duration", event.Duration()))
	ids := event.Correlation()
	if ids.TraceID != (ID{}) {
		record.AddAttrs(slog.String("trace_id", ids.TraceID.String()))
	}
	if ids.SpanID != (SpanID{}) {
		record.AddAttrs(slog.String("span_id", ids.SpanID.String()))
	}
	if event.ParentSpanID() != (SpanID{}) {
		record.AddAttrs(slog.String("parent_span_id", event.ParentSpanID().String()))
	}
	if ids.RequestID != (ID{}) {
		record.AddAttrs(slog.String("request_id", ids.RequestID.String()))
	}
	if ids.TaskID != (ID{}) {
		record.AddAttrs(slog.String("task_id", ids.TaskID.String()))
	}
	if s.handler.Handle(ctx, record) != nil {
		return ErrExporter
	}
	return nil
}

func (s *SlogExporter) Flush(ctx context.Context) error { _, err := inspectContext(ctx); return err }
func (s *SlogExporter) Close(ctx context.Context) error { _, err := inspectContext(ctx); return err }
