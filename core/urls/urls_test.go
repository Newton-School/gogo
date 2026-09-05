package urls

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestIncludeReverseResolveMethods(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, Param(r, "id")) })
	router, e := New(Include("api/", "catalog", Path("products/<int:id>/", h, "detail", "GET")))
	if e != nil {
		t.Fatal(e)
	}
	path, e := router.Reverse("catalog:detail", map[string]any{"id": int64(42)}, url.Values{"q": {"a&b"}})
	if e != nil || path != "/api/products/42/?q=a%26b" {
		t.Fatal(path, e)
	}
	for _, test := range []struct {
		method, path string
		status       int
		body         string
	}{{"GET", "/api/products/42/", 200, "42"}, {"POST", "/api/products/42/", 405, "method not allowed\n"}, {"HEAD", "/api/products/42/", 200, ""}, {"OPTIONS", "/api/products/42/", 204, ""}, {"GET", "/api/products/no/", 404, "404 page not found\n"}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != test.status || w.Body.String() != test.body {
			t.Fatal(test, w.Code, w.Body)
		}
	}
}
func TestInvalidNamesAndReverseArguments(t *testing.T) {
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if _, e := New(Path("a", h, "same"), Path("b", h, "same")); e == nil {
		t.Fatal("duplicate accepted")
	}
	r, e := New(Path("<str:value>/", h, "one"))
	if e != nil {
		t.Fatal(e)
	}
	for _, params := range []map[string]any{{}, {"value": "a/b"}, {"value": "a", "extra": 1}} {
		if _, e = r.Reverse("one", params, nil); e == nil {
			t.Fatal(params)
		}
	}
}
