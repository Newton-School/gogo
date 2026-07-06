package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/auth"
)

func TestAdminSearchBuildsSQLForPrefixesAndRelatedFields(t *testing.T) {
	search, err := BuildSearchQuery(SearchOptions{
		Fields:  []string{"title", "=slug", "^code", "@body", "author__name"},
		Term:    "Gogo",
		Dialect: "postgres",
	})
	if err != nil {
		t.Fatalf("BuildSearchQuery() error = %v", err)
	}

	wantSQL := `(LOWER(title) LIKE LOWER(?) OR slug = ? OR LOWER(code) LIKE LOWER(?) OR to_tsvector(body) @@ plainto_tsquery(?) OR LOWER(author.name) LIKE LOWER(?))`
	if search.Where != wantSQL {
		t.Fatalf("Where = %q, want %q", search.Where, wantSQL)
	}
	if !reflect.DeepEqual(search.Args, []any{"%Gogo%", "Gogo", "Gogo%", "Gogo", "%Gogo%"}) {
		t.Fatalf("Args = %#v", search.Args)
	}
	if !search.MayHaveDuplicates {
		t.Fatalf("related search should mark possible duplicates")
	}

	sqlite, err := BuildSearchQuery(SearchOptions{Fields: []string{"@body"}, Term: "Gogo", Dialect: "sqlite"})
	if err != nil {
		t.Fatalf("BuildSearchQuery(sqlite) error = %v", err)
	}
	if sqlite.Where != `(LOWER(body) LIKE LOWER(?))` {
		t.Fatalf("sqlite full-text fallback = %q", sqlite.Where)
	}
}

func TestAdminFiltersExposeChoicesFacetsAndApplyRows(t *testing.T) {
	rows := []map[string]any{
		{"id": 1, "is_active": true, "status": "draft", "created_at": time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), "author": "saksham", "archived_at": nil},
		{"id": 2, "is_active": false, "status": "published", "created_at": time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC), "author": "fatih", "archived_at": time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"id": 3, "is_active": true, "status": "draft", "created_at": time.Date(2025, 1, 10, 0, 0, 0, 0, time.UTC), "author": "saksham", "archived_at": nil},
	}
	filters := []FilterSpec{
		BooleanFilter("is_active", "Active"),
		ChoicesFilter("status", "Status", []FilterChoice{{Value: "draft", Label: "Draft"}, {Value: "published", Label: "Published"}}),
		DateFilter("created_at", "Created"),
		RelatedFilter("author", "Author", []FilterChoice{{Value: "saksham", Label: "Saksham"}, {Value: "fatih", Label: "Fatih"}}),
		EmptyFieldFilter("archived_at", "Archived"),
		SimpleListFilter("mine", "Mine", []FilterChoice{{Value: "1", Label: "Mine"}}, func(row map[string]any, value string) bool {
			return value == "1" && row["author"] == "saksham"
		}),
	}

	result := BuildFilters(filters, rows, url.Values{"status": {"draft"}, "is_active": {"1"}, "created_at__year": {"2026"}, "author": {"saksham"}, "archived_at__empty": {"1"}, "mine": {"1"}}, true)
	if got := filterTitles(result.Filters); !reflect.DeepEqual(got, []string{"Active", "Status", "Created", "Author", "Archived", "Mine"}) {
		t.Fatalf("filter titles = %#v", got)
	}
	if !reflect.DeepEqual(result.Rows, []map[string]any{rows[0]}) {
		t.Fatalf("filtered rows = %#v", result.Rows)
	}
	status := result.Filters[1]
	if !reflect.DeepEqual(status.Choices, []FilterChoice{{Value: "draft", Label: "Draft", Selected: true, Count: 2}, {Value: "published", Label: "Published", Count: 1}}) {
		t.Fatalf("status choices = %#v", status.Choices)
	}
	date := result.Filters[2]
	if !reflect.DeepEqual(date.Choices, []FilterChoice{{Value: "2025", Label: "2025", Count: 1}, {Value: "2026", Label: "2026", Selected: true, Count: 2}}) {
		t.Fatalf("date choices = %#v", date.Choices)
	}
}

func TestAutocompleteEndpointChecksPermissionSearchesPaginatesAndForwards(t *testing.T) {
	endpoint := AutocompleteEndpoint(AutocompleteConfig{
		SearchFields: []string{"title"},
		PageSize:     1,
		Rows: []map[string]any{
			{"id": 1, "title": "Gogo Admin", "category": "framework"},
			{"id": 2, "title": "Gogo API", "category": "framework"},
			{"id": 3, "title": "Other", "category": "misc"},
		},
		ForwardedConstraints: map[string]string{"category": "framework"},
		HasPermission: func(*http.Request, auth.User) bool {
			return true
		},
	})
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, Authenticated: true}}}
	request := httptest.NewRequest("GET", "/admin/blog/post/autocomplete/?q=Gogo&page=1&forward_category=framework", nil)
	request = request.WithContext(auth.ContextWithUser(request.Context(), user))
	recorder := httptest.NewRecorder()
	endpoint.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload AutocompleteResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json decode error = %v", err)
	}
	if !payload.Pagination.More {
		t.Fatalf("pagination = %#v", payload.Pagination)
	}
	if !reflect.DeepEqual(payload.Results, []AutocompleteResult{{ID: "1", Text: "Gogo Admin"}}) {
		t.Fatalf("results = %#v", payload.Results)
	}

	denied := AutocompleteEndpoint(AutocompleteConfig{HasPermission: func(*http.Request, auth.User) bool { return false }})
	deniedRecorder := httptest.NewRecorder()
	denied.ServeHTTP(deniedRecorder, request)
	if deniedRecorder.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want 403", deniedRecorder.Code)
	}
}

func filterTitles(filters []FilterState) []string {
	titles := make([]string, len(filters))
	for i, filter := range filters {
		titles[i] = filter.Title
	}
	return titles
}
