package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResponseValidationAndConditional(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	response := Text(200, "ok")
	response.Headers.Set("X-Test", "bad\r\nInjected: true")
	if response.Write(w, r) == nil || w.Body.Len() != 0 {
		t.Fatal("unsafe header")
	}
	response = Text(200, "ok")
	response.ETag = `"version-1"`
	r.Header.Set("If-None-Match", `W/"version-1"`)
	if e := response.Write(w, r); e != nil || w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal(w.Code, e)
	}
}
func TestRecoveryRedactsAndDoesNotAppendAfterHeaders(t *testing.T) {
	h := Recovery(Adapt(func(*http.Request) (Response, error) { panic("private-password") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private-password") {
		t.Fatal(w.Body)
	}
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("stream panic not aborted")
		}
	}()
	Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("begun")); panic("private") })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}
func TestSSECancellationAndMultiline(t *testing.T) {
	events := make(chan Event, 1)
	events <- Event{ID: "1", Data: "one\r\ntwo"}
	close(events)
	w := httptest.NewRecorder()
	if err := SSE(w, httptest.NewRequest("GET", "/", nil), events, SSEConfig{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "data: one\ndata: two\n\n") {
		t.Fatal(w.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SSE(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(ctx), make(chan Event), SSEConfig{IdleTimeout: time.Second}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
