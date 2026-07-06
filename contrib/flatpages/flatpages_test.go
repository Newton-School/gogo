package flatpages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/contrib/sites"
)

func TestFlatpageLookupViewSiteTemplateRegistrationAndAdmin(t *testing.T) {
	store := NewMemoryStore([]FlatPage{
		{URL: "/about/", Title: "About", Content: "Default content", SiteIDs: []int{1}},
		{URL: "/custom/", Title: "Custom", Content: "Custom content", TemplateName: "custom.html", SiteIDs: []int{1}},
		{URL: "/private/", Title: "Private", Content: "Private content", RegistrationRequired: true, SiteIDs: []int{1}},
	})
	siteStore := sites.NewMemoryStore([]sites.Site{{ID: 1, Domain: "example.com"}, {ID: 2, Domain: "other.com"}})
	page, ok := store.Find("/about/", 1)
	if !ok || page.Title != "About" {
		t.Fatalf("Find() = %#v, %v", page, ok)
	}
	view := View(store, siteStore, Options{Templates: map[string]string{"custom.html": "custom: {{title}} {{content}}"}, Authenticated: func(*http.Request) bool { return false }})
	assertFlatpage(t, view, "http://example.com/about/", http.StatusOK, "Default content")
	assertFlatpage(t, view, "http://example.com/custom/", http.StatusOK, "custom: Custom Custom content")
	assertFlatpage(t, view, "http://example.com/private/", http.StatusForbidden, "")
	assertFlatpage(t, view, "http://other.com/about/", http.StatusNotFound, "")
	assertFlatpage(t, view, "http://example.com/missing/", http.StatusNotFound, "")
	if Admin().Model.ModelName != "FlatPage" || len(Admin().Fieldsets) == 0 {
		t.Fatalf("admin = %#v", Admin())
	}
	if Migration().Name != "0001_initial" {
		t.Fatalf("migration missing")
	}
}

func assertFlatpage(t *testing.T, handler http.Handler, target string, status int, contains string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != status {
		t.Fatalf("%s status=%d want=%d body=%q", target, recorder.Code, status, recorder.Body.String())
	}
	if contains != "" && !strings.Contains(recorder.Body.String(), contains) {
		t.Fatalf("%s body=%q missing %q", target, recorder.Body.String(), contains)
	}
}
