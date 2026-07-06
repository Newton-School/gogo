package http

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/conf"
)

func TestMiddlewareChainPreservesOrder(t *testing.T) {
	var order []string
	final := Handler(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {
		order = append(order, "handler")
	}))

	handler := Chain(final, recordingMiddleware("one", &order), recordingMiddleware("two", &order))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))

	want := []string{"one:before", "two:before", "handler", "two:after", "one:after"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}

func TestMiddlewareCanShortCircuit(t *testing.T) {
	called := false
	final := Handler(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {
		called = true
	}))
	shortCircuit := func(Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.WriteHeader(418)
			_, _ = w.Write([]byte("stopped"))
		})
	}

	recorder := httptest.NewRecorder()
	Chain(final, shortCircuit).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))

	if called {
		t.Fatalf("final handler was called after short-circuit")
	}
	if recorder.Code != 418 || recorder.Body.String() != "stopped" {
		t.Fatalf("response = (%d, %q), want (418, stopped)", recorder.Code, recorder.Body.String())
	}
}

func TestBuildMiddlewareUsesSettingsOrder(t *testing.T) {
	settings := conf.Settings{Middleware: []string{"one", "two"}}
	var order []string
	registry := MiddlewareRegistry{
		"one": func(conf.Settings) Middleware { return recordingMiddleware("one", &order) },
		"two": func(conf.Settings) Middleware { return recordingMiddleware("two", &order) },
	}

	middleware, err := BuildMiddleware(settings, registry)
	if err != nil {
		t.Fatalf("BuildMiddleware() error = %v", err)
	}
	final := Handler(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {
		order = append(order, "handler")
	}))
	Chain(final, middleware...).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	want := []string{"one:before", "two:before", "handler", "two:after", "one:after"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}

func TestRequestIDMiddlewareSetsHeaderAndContext(t *testing.T) {
	var seen string
	final := Handler(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(204)
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set(RequestIDHeader, "req-123")
	Chain(final, RequestIDMiddleware()).ServeHTTP(recorder, request)

	if seen != "req-123" {
		t.Fatalf("request id in context = %q, want req-123", seen)
	}
	if got := recorder.Header().Get(RequestIDHeader); got != "req-123" {
		t.Fatalf("response request id = %q, want req-123", got)
	}
}

func TestPanicRecoveryMiddlewareReturnsSafe500(t *testing.T) {
	final := Handler(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {
		panic("private detail")
	}))

	recorder := httptest.NewRecorder()
	Chain(final, PanicRecoveryMiddleware()).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))

	if recorder.Code != 500 {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("private detail")) {
		t.Fatalf("panic detail leaked in response body")
	}
}

func TestAccessLogMiddlewareWritesStructuredFields(t *testing.T) {
	var log bytes.Buffer
	final := Handler(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte("created"))
	}))

	request := httptest.NewRequest("POST", "/objects/", nil)
	request.Header.Set(RequestIDHeader, "req-456")
	Chain(final, RequestIDMiddleware(), AccessLogMiddleware(&log)).ServeHTTP(httptest.NewRecorder(), request)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(log.Bytes()), &entry); err != nil {
		t.Fatalf("log entry is not JSON: %v", err)
	}
	if entry["method"] != "POST" || entry["path"] != "/objects/" || entry["request_id"] != "req-456" {
		t.Fatalf("log entry = %#v, want method/path/request_id fields", entry)
	}
	if entry["status"].(float64) != 201 {
		t.Fatalf("status = %#v, want 201", entry["status"])
	}
}

func TestAccessLogMiddlewareIncludesFrameworkRouteName(t *testing.T) {
	router := NewRouter()
	if err := router.Handle("object-list", "/objects/", func(_ context.Context, _ *Request) Response {
		return Text(nethttp.StatusAccepted, "accepted")
	}, nethttp.MethodGet); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	var log bytes.Buffer
	Chain(router, AccessLogMiddleware(&log)).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(nethttp.MethodGet, "/objects/", nil))

	entry := decodeAccessLogEntry(t, log.Bytes())
	if entry["route_name"] != "object-list" {
		t.Fatalf("route_name = %#v, want object-list in entry %#v", entry["route_name"], entry)
	}
}

func TestAccessLogMiddlewareIncludesRawRouteName(t *testing.T) {
	router := NewRouter()
	if err := router.HandleHTTP("stream-events", "/events/", nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusNoContent)
	}), nethttp.MethodGet); err != nil {
		t.Fatalf("HandleHTTP() error = %v", err)
	}

	var log bytes.Buffer
	Chain(router, AccessLogMiddleware(&log)).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(nethttp.MethodGet, "/events/", nil))

	entry := decodeAccessLogEntry(t, log.Bytes())
	if entry["route_name"] != "stream-events" {
		t.Fatalf("route_name = %#v, want stream-events in entry %#v", entry["route_name"], entry)
	}
}

func TestHostValidationMiddlewareRejectsUnexpectedHost(t *testing.T) {
	final := Handler(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(204)
	}))
	handler := Chain(final, HostValidationMiddleware([]string{"example.com", ".example.org", "::1"}))

	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, httptest.NewRequest("GET", "http://api.example.org/", nil))
	if allowed.Code != 204 {
		t.Fatalf("allowed status = %d, want 204", allowed.Code)
	}

	ipv6 := httptest.NewRecorder()
	handler.ServeHTTP(ipv6, httptest.NewRequest("GET", "http://[::1]/", nil))
	if ipv6.Code != 204 {
		t.Fatalf("ipv6 status = %d, want 204", ipv6.Code)
	}

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest("GET", "http://evil.test/", nil))
	if rejected.Code != 400 {
		t.Fatalf("rejected status = %d, want 400", rejected.Code)
	}
}

func decodeAccessLogEntry(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &entry); err != nil {
		t.Fatalf("log entry is not JSON: %v", err)
	}
	return entry
}

func recordingMiddleware(name string, order *[]string) Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			*order = append(*order, name+":before")
			next.ServeHTTP(w, r)
			*order = append(*order, name+":after")
		})
	}
}
