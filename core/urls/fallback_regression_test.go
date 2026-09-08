package urls

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotFoundFallbackCapturesHandlerBeforeConverter(t *testing.T) {
	var router *Router
	replacement, err := New()
	if err != nil {
		t.Fatal(err)
	}
	replacement, err = replacement.WithNotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(418) }))
	if err != nil {
		t.Fatal(err)
	}
	converters := Builtins()
	decoded := 0
	converters["rejected"] = Converter{Pattern: "[a-z]+", Decode: func(string) (any, error) { decoded++; *router = *replacement; return nil, errors.New("not accepted") }, Encode: func(any) (string, error) { return "", errors.New("unused") }}
	base, err := NewWithConverters(converters, Path("/<rejected:value>", http.NotFoundHandler(), "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	router, err = base.WithNotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(410) }))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 410 || decoded != 1 {
		t.Fatalf("converter retargeted current dispatch fallback: %d, decoded %d", w.Code, decoded)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 418 || decoded != 1 {
		t.Fatal("next dispatch did not use replacement router", w.Code, decoded)
	}
}

func TestNotFoundFallbackNestedMissDoesNotInheritOuterMatch(t *testing.T) {
	type callerContextKey struct{}
	marker := &struct{}{}
	inner, err := New()
	if err != nil {
		t.Fatal(err)
	}
	inner, err = inner.WithNotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Name(r) != "" || len(Params(r)) != 0 {
			t.Errorf("route miss inherited outer match: %q %v", Name(r), Params(r))
		}
		if r.Context().Value(callerContextKey{}) != marker {
			t.Error("fallback lost caller context")
		}
		w.WriteHeader(410)
	}))
	if err != nil {
		t.Fatal(err)
	}
	outer, err := New(Path("/<path:remaining>", inner, "outer"))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/missing", nil)
	request = request.WithContext(context.WithValue(request.Context(), callerContextKey{}, marker))
	outer.ServeHTTP(w, request)
	if w.Code != 410 {
		t.Fatal(w.Code)
	}
}
