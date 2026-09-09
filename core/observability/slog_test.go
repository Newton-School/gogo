package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestConsoleExporterHasOnlyFixedKeys(t *testing.T) {
	var output bytes.Buffer
	exporter, err := NewConsoleExporter(&output, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestPipeline(t, exporter, nil)
	ctx, _ := WithCorrelation(context.Background(), Correlation{TraceID: ID{1}, SpanID: SpanID{2}, RequestID: ID{3}, TaskID: ID{4}})
	_, span, err := p.Start(ctx, "db.select", "")
	if err != nil {
		t.Fatal(err)
	}
	span.End(StatusFailure)
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"time": true, "level": true, "msg": true, "operation": true, "route": true, "status": true, "duration": true, "trace_id": true, "span_id": true, "parent_span_id": true, "request_id": true, "task_id": true}
	for key := range event {
		if !allowed[key] {
			t.Fatalf("unexpected field %q", key)
		}
	}
	if len(event) != len(allowed) || event["operation"] != "db.select" || event["status"] != "failure" || event["msg"] != "gogo.operation" {
		t.Fatal(event)
	}
	for key, length := range map[string]int{"trace_id": 32, "span_id": 16, "parent_span_id": 16, "request_id": 32, "task_id": 32} {
		value, ok := event[key].(string)
		if !ok || len(value) != length || strings.Trim(value, "0123456789abcdef") != "" {
			t.Fatal(key, value)
		}
	}
}

type badWriter struct{ panic bool }

func (writer badWriter) Write([]byte) (int, error) {
	if writer.panic {
		panic("private-writer-marker")
	}
	return 0, errors.New("private-writer-marker")
}

func TestConsoleWriterFailureIsRedactedAndCounted(t *testing.T) {
	for _, panics := range []bool{false, true} {
		exporter, err := NewConsoleExporter(badWriter{panic: panics}, slog.LevelInfo)
		if err != nil {
			t.Fatal(err)
		}
		p := newTestPipeline(t, exporter, nil)
		if !startTestSpan(t, p).End(StatusSuccess) {
			t.Fatal("admission failed")
		}
		if err := p.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		if stats := p.Stats(); stats.Dropped != 1 || stats.ExportFailures != 1 {
			t.Fatal(stats)
		}
	}
	if _, err := NewConsoleExporter(nil, slog.LevelInfo); err != ErrInvalidConfig {
		t.Fatal(err)
	}
	if _, err := NewConsoleExporter(&bytes.Buffer{}, slog.Level(3)); err != ErrInvalidConfig {
		t.Fatal(err)
	}
	if _, err := NewSlogExporter(nil); err != ErrInvalidConfig {
		t.Fatal(err)
	}
}

type countWriter func([]byte) (int, error)

func (writer countWriter) Write(data []byte) (int, error) { return writer(data) }

func TestConsoleWriterRejectsShortAndInvalidNilErrorCounts(t *testing.T) {
	for name, writer := range map[string]countWriter{
		"zero":         func([]byte) (int, error) { return 0, nil },
		"short":        func(data []byte) (int, error) { return len(data) - 1, nil },
		"negative":     func([]byte) (int, error) { return -1, nil },
		"overreported": func(data []byte) (int, error) { return len(data) + 1, nil },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (checkedWriter{writer: writer}).Write([]byte("event")); err != io.ErrShortWrite {
				t.Fatal(err)
			}
			exporter, err := NewConsoleExporter(writer, slog.LevelInfo)
			if err != nil {
				t.Fatal(err)
			}
			if err := exporter.Export(context.Background(), Event{operation: "db.select"}); err != ErrExporter {
				t.Fatal(err)
			}
			p := newTestPipeline(t, exporter, nil)
			if !startTestSpan(t, p).End(StatusSuccess) {
				t.Fatal("admission failed")
			}
			if err := p.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if stats := p.Stats(); stats.Exported != 0 || stats.Dropped != 1 || stats.ExportFailures != 1 {
				t.Fatal(stats)
			}
		})
	}
}
