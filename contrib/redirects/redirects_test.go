package redirects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Newton-School/gogo/contrib/sites"
)

func TestRedirectMiddlewareStatusUnsafeAndSiteFiltering(t *testing.T) {
	siteStore := sites.NewMemoryStore([]sites.Site{{ID: 1, Domain: "example.com"}, {ID: 2, Domain: "other.com"}})
	store := NewMemoryStore([]Redirect{
		{SiteID: 1, OldPath: "/old", NewPath: "/new", Permanent: true},
		{SiteID: 1, OldPath: "/temp", NewPath: "/later", Permanent: false},
		{SiteID: 1, OldPath: "/gone", NewPath: "", Permanent: true},
		{SiteID: 2, OldPath: "/old", NewPath: "/other", Permanent: true},
		{SiteID: 1, OldPath: "/unsafe", NewPath: "https://evil.example.com", Permanent: true},
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exists" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})
	middleware := Middleware(store, siteStore, Options{})
	assertStatusLocation(t, middleware(next), "http://example.com/old", http.StatusMovedPermanently, "/new")
	assertStatusLocation(t, middleware(next), "http://example.com/temp", http.StatusFound, "/later")
	assertStatusLocation(t, middleware(next), "http://example.com/gone", http.StatusGone, "")
	assertStatusLocation(t, middleware(next), "http://example.com/exists", http.StatusOK, "")
	assertStatusLocation(t, middleware(next), "http://other.com/old", http.StatusMovedPermanently, "/other")
	assertStatusLocation(t, middleware(next), "http://example.com/unsafe", http.StatusNotFound, "")

	unsafeAllowed := Middleware(store, siteStore, Options{AllowUnsafeTargets: true})
	assertStatusLocation(t, unsafeAllowed(next), "http://example.com/unsafe", http.StatusMovedPermanently, "https://evil.example.com")
	if Admin().Model.ModelName != "Redirect" || Migration().Name != "0001_initial" {
		t.Fatalf("admin/migration missing")
	}
	if AppConfig().Label() != "redirects" {
		t.Fatalf("app config = %#v", AppConfig())
	}
	_, _ = CurrentRedirect(context.Background(), httptest.NewRequest(http.MethodGet, "http://example.com/old", nil), store, siteStore)
}

func assertStatusLocation(t *testing.T, handler http.Handler, target string, status int, location string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != status {
		t.Fatalf("%s status = %d, want %d", target, recorder.Code, status)
	}
	if location != "" && recorder.Header().Get("Location") != location {
		t.Fatalf("%s location = %q", target, recorder.Header().Get("Location"))
	}
}
