package admin

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

func TestModelAdminOptionsNormalizeAndClone(t *testing.T) {
	admin := ModelAdmin{
		Actions:                 []string{"publish"},
		AutocompleteFields:      []string{"author"},
		DateHierarchy:           "created_at",
		EmptyValueDisplay:       "(empty)",
		Exclude:                 []string{"internal"},
		Fields:                  []string{"title", "slug"},
		Fieldsets:               []Fieldset{{Name: "Main", Fields: []string{"title"}}},
		FilterHorizontal:        []string{"tags"},
		FilterVertical:          []string{"groups"},
		Form:                    "PostForm",
		FormfieldOverrides:      map[string]string{"text": "Textarea"},
		Inlines:                 []Inline{{Model: "blog.Comment", Kind: InlineTabular, Extra: 1}},
		ListDisplay:             []string{"title", "status"},
		ListDisplayLinks:        []string{"title"},
		ListEditable:            []string{"status"},
		ListFilter:              []string{"status"},
		ListMaxShowAll:          500,
		ListPerPage:             50,
		ListSelectRelated:       []string{"author"},
		Ordering:                []string{"-created_at"},
		Paginator:               "DefaultPaginator",
		PrepopulatedFields:      map[string][]string{"slug": {"title"}},
		PreserveFilters:         true,
		RadioFields:             map[string]string{"status": "horizontal"},
		RawIDFields:             []string{"author"},
		ReadonlyFields:          []string{"created_at"},
		SaveAs:                  true,
		SaveAsContinue:          true,
		SaveOnTop:               true,
		SearchFields:            []string{"title"},
		SearchHelpText:          "Search posts",
		ShowFacets:              true,
		SortableBy:              []string{"title"},
		ViewOnSite:              true,
		ActionsOnTop:            true,
		ActionsOnBottom:         true,
		ActionsSelectionCounter: true,
		CustomURLs:              []URLPattern{{Name: "publish", Path: "publish/"}},
	}
	normalized := admin.Normalize()
	normalized.ListDisplay[0] = "changed"
	normalized.FormfieldOverrides["text"] = "Changed"
	normalized.PrepopulatedFields["slug"][0] = "changed"

	again := admin.Normalize()
	if again.ListDisplay[0] != "title" || again.FormfieldOverrides["text"] != "Textarea" || again.PrepopulatedFields["slug"][0] != "title" {
		t.Fatalf("Normalize leaked mutable options: %#v", again)
	}
	if again.ListPerPage != 50 || again.EmptyValueDisplay != "(empty)" || !again.ActionsSelectionCounter {
		t.Fatalf("normalized defaults/options = %#v", again)
	}
}

func TestModelAdminValidationRejectsInvalidEditableFields(t *testing.T) {
	meta := models.Metadata{AppLabel: "blog", ModelName: "Post"}
	admin := ModelAdmin{ListDisplay: []string{"title"}, ListEditable: []string{"status"}}
	if err := admin.Validate(meta); err == nil {
		t.Fatalf("Validate() error = nil, want invalid list_editable")
	}

	admin = ModelAdmin{ListDisplay: []string{"title", "status"}, ListDisplayLinks: []string{"status"}, ListEditable: []string{"status"}}
	if err := admin.Validate(meta); err == nil {
		t.Fatalf("Validate() error = nil, want editable display link rejection")
	}

	admin = ModelAdmin{ListDisplay: []string{"title", "status"}, ListDisplayLinks: []string{"title"}, ListEditable: []string{"status"}}
	if err := admin.Validate(meta); err != nil {
		t.Fatalf("Validate(valid) error = %v", err)
	}
}

func TestModelAdminHooksAndCustomURLs(t *testing.T) {
	request := httptest.NewRequest("GET", "/admin/blog/post/", nil)
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, Authenticated: true}}}
	request = request.WithContext(auth.ContextWithUser(request.Context(), user))
	admin := ModelAdmin{
		Ordering:       []string{"title"},
		ListDisplay:    []string{"title"},
		ReadonlyFields: []string{"created_at"},
		CustomURLs:     []URLPattern{{Name: "stats", Path: "stats/", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}},
		Hooks: ModelAdminHooks{
			GetOrdering:       func(*http.Request) []string { return []string{"-created_at"} },
			GetListDisplay:    func(*http.Request) []string { return []string{"title", "status"} },
			GetReadonlyFields: func(*http.Request) []string { return []string{"created_at", "updated_at"} },
			HasViewPermission: func(*http.Request, auth.User) bool { return true },
			GetURLs:           func(*http.Request) []URLPattern { return []URLPattern{{Name: "preview", Path: "preview/"}} },
		},
	}

	if got := admin.GetOrdering(request); !reflect.DeepEqual(got, []string{"-created_at"}) {
		t.Fatalf("GetOrdering() = %#v", got)
	}
	if got := admin.GetListDisplay(request); !reflect.DeepEqual(got, []string{"title", "status"}) {
		t.Fatalf("GetListDisplay() = %#v", got)
	}
	if got := admin.GetReadonlyFields(request); !reflect.DeepEqual(got, []string{"created_at", "updated_at"}) {
		t.Fatalf("GetReadonlyFields() = %#v", got)
	}
	if !admin.HasViewPermission(request, user) {
		t.Fatalf("HasViewPermission() = false")
	}
	if got := admin.GetURLs(request); !reflect.DeepEqual(urlNames(got), []string{"stats", "preview"}) {
		t.Fatalf("GetURLs() = %#v", got)
	}
}

func TestModelAdminReadOnlyAllowsViewAndBlocksMutations(t *testing.T) {
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, Authenticated: true}, IsStaff: true}}
	request := httptest.NewRequest(http.MethodGet, "/admin/blog/post/", nil)
	modelAdmin := ModelAdmin{ReadOnly: true}
	if !modelAdmin.HasViewPermission(request, user) || !modelAdmin.HasModulePermission(request, user) {
		t.Fatalf("read-only admin should allow staff view/module access")
	}
	if modelAdmin.HasAddPermission(request, user) || modelAdmin.HasChangePermission(request, user) || modelAdmin.HasDeletePermission(request, user) {
		t.Fatalf("read-only admin allowed mutation")
	}
}

func urlNames(urls []URLPattern) []string {
	names := make([]string, len(urls))
	for i, url := range urls {
		names[i] = url.Name
	}
	return names
}
