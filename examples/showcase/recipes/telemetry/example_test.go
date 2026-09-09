package observability_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/Newton-School/gogo/core/observability"
)

func Example_telemetryPipeline() {
	exporter, err := observability.NewConsoleExporter(io.Discard, slog.LevelInfo)
	if err != nil {
		panic(err)
	}
	telemetry, err := observability.New(observability.Config{
		Operations: []string{"books.list"},
		Routes:     []string{"/books/"},
		SampleRate: observability.SampleAll,
	}, exporter)
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	_, span, err := telemetry.Start(ctx, "books.list", "/books/")
	if err != nil {
		panic(err)
	}
	fmt.Println(span.End(observability.StatusHTTP2xx))
	if err := telemetry.Close(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println(telemetry.Stats().Exported)
	// Output:
	// true
	// 1
}
