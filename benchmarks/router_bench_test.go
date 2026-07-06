package benchmarks

import (
	"context"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	gogohttp "github.com/Newton-School/gogo/http"
)

func BenchmarkRouterMatch(b *testing.B) {
	router := gogohttp.NewRouter()
	view := func(context.Context, *gogohttp.Request) gogohttp.Response {
		return gogohttp.Text(nethttp.StatusOK, "ok")
	}
	for i := 0; i < 64; i++ {
		if err := router.Handle(fmt.Sprintf("section-%d", i), fmt.Sprintf("/section-%d/<int:id>/", i), view, nethttp.MethodGet); err != nil {
			b.Fatalf("Handle(section-%d) error = %v", i, err)
		}
	}
	if err := router.Handle("comment-detail", "/posts/<int:post_id>/comments/<int:id>/", view, nethttp.MethodGet); err != nil {
		b.Fatalf("Handle(comment-detail) error = %v", err)
	}
	request := httptest.NewRequest(nethttp.MethodGet, "/posts/42/comments/99/", nil)
	writer := &discardResponseWriter{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		writer.Reset()
		router.ServeHTTP(writer, request)
		if writer.status != nethttp.StatusOK {
			b.Fatalf("status = %d, want %d", writer.status, nethttp.StatusOK)
		}
	}
}

func BenchmarkRouterReverse(b *testing.B) {
	router := gogohttp.NewRouter()
	view := func(context.Context, *gogohttp.Request) gogohttp.Response {
		return gogohttp.Text(nethttp.StatusOK, "ok")
	}
	if err := router.Handle("comment-detail", "/posts/<int:post_id>/comments/<int:id>/", view, nethttp.MethodGet); err != nil {
		b.Fatalf("Handle() error = %v", err)
	}
	args := map[string]any{"post_id": 42, "id": 99}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path, err := router.Reverse("comment-detail", args)
		if err != nil {
			b.Fatalf("Reverse() error = %v", err)
		}
		if path != "/posts/42/comments/99/" {
			b.Fatalf("path = %q", path)
		}
	}
}

type discardResponseWriter struct {
	header nethttp.Header
	status int
}

func (w *discardResponseWriter) Header() nethttp.Header {
	if w.header == nil {
		w.header = nethttp.Header{}
	}
	return w.header
}

func (w *discardResponseWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func (w *discardResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *discardResponseWriter) Reset() {
	w.header = nethttp.Header{}
	w.status = 0
}
