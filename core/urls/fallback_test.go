package urls

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNotFoundFallbackPreservesMatchedViewsAndMethodDispatch(t *testing.T) {
	var misses, decoded int
	converters := Builtins()
	converters["checked"] = Converter{Pattern: "[a-z]+", Decode: func(value string) (any, error) { decoded++; return value, nil }, Encode: func(value any) (string, error) { return value.(string), nil }}
	base, err := NewWithConverters(converters, Path("/<checked:word>", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Name(r) != "checked" || Param(r, "word") != "found" {
			t.Fatal("match context lost")
		}
		w.Header().Set("X-Matched", "yes")
		http.NotFound(w, r)
	}), "checked", http.MethodGet))
	if err != nil {
		t.Fatal(err)
	}
	router, err := base.WithNotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		misses++
		if Name(r) != "" || len(Params(r)) != 0 {
			t.Fatal("route miss received match metadata")
		}
		w.Header().Set("Location", "/replacement")
		w.WriteHeader(http.StatusMovedPermanently)
		_, _ = fmt.Fprint(w, "fallback")
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path           string
		status, decodes, calls int
	}{
		{"GET", "/found", 404, 1, 0}, {"POST", "/found", 405, 1, 0}, {"OPTIONS", "/found", 204, 1, 0},
		{"GET", "/123", 301, 0, 1}, {"HEAD", "/123", 301, 0, 1},
	} {
		decoded, misses = 0, 0
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != test.status || decoded != test.decodes || misses != test.calls {
			t.Fatal(test, w.Code, decoded, misses)
		}
		if test.calls == 0 && w.Header().Get("Location") != "" {
			t.Fatal("fallback intercepted matched response")
		}
		if test.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD fallback emitted a body")
		}
		if test.status == 405 && w.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
			t.Fatal(w.Header())
		}
	}
	w := httptest.NewRecorder()
	base.ServeHTTP(w, httptest.NewRequest("GET", "/123", nil))
	if w.Code != 404 || strings.Contains(w.Body.String(), "fallback") {
		t.Fatal("original router changed", w)
	}
	if path, err := router.Reverse("checked", map[string]any{"word": "found"}, nil); err != nil || path != "/found" {
		t.Fatal(path, err)
	}
	if _, err := router.Resolve("/123"); !errors.Is(err, ErrNotFound) {
		t.Fatal("fallback changed Resolve contract", err)
	}
}

func TestNotFoundFallbackRejectsInvalidConfigurationAndRemainsIndependent(t *testing.T) {
	var absent *Router
	if got, err := absent.WithNotFound(http.NotFoundHandler()); err == nil || got != nil {
		t.Fatal(got, err)
	}
	base, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, handler := range []http.Handler{nil, http.HandlerFunc(nil)} {
		if got, err := base.WithNotFound(handler); err == nil || got != nil {
			t.Fatal(got, err)
		}
	}
	var calls atomic.Int64
	first, err := base.WithNotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(410) }))
	if err != nil {
		t.Fatal(err)
	}
	second, err := first.WithNotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(418) }))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, test := range []struct {
				router *Router
				status int
			}{{base, 404}, {first, 410}, {second, 418}} {
				w := httptest.NewRecorder()
				test.router.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
				if w.Code != test.status {
					t.Errorf("got %d, want %d", w.Code, test.status)
				}
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 20 {
		t.Fatal(calls.Load())
	}
}
