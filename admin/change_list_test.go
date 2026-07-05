package admin

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestChangeListBuildsDisplayRowsSortingPaginationAndFilters(t *testing.T) {
	admin := ModelAdmin{
		ListDisplay:       []string{"title", "published", "summary"},
		ListEditable:      []string{"published"},
		ListPerPage:       1,
		ListMaxShowAll:    5,
		EmptyValueDisplay: "(none)",
		DateHierarchy:     "created_at",
		ActionDefinitions: []Action{{Name: "publish", Label: "Publish"}},
		ComputedColumns: map[string]ComputedColumn{
			"summary": func(row map[string]any) any { return row["title"].(string) + "!" },
		},
	}
	rows := []map[string]any{
		{"id": 1, "title": "Beta", "published": false, "created_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"id": 2, "title": "Alpha", "published": true, "created_at": time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"id": 3, "title": "", "published": true, "created_at": time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
	}
	changeList, err := BuildChangeList(admin, rows, url.Values{"o": {"title"}, "p": {"2"}, "status": {"draft"}, "_popup": {"1"}})
	if err != nil {
		t.Fatalf("BuildChangeList() error = %v", err)
	}

	if !reflect.DeepEqual(changeList.Columns, []ChangeListColumn{{Name: "title", Link: true}, {Name: "published", Editable: true}, {Name: "summary", Computed: true}}) {
		t.Fatalf("columns = %#v", changeList.Columns)
	}
	if changeList.Total != 3 || changeList.Page != 2 || changeList.PerPage != 1 || len(changeList.Rows) != 1 {
		t.Fatalf("pagination = %#v", changeList)
	}
	if got := changeList.Rows[0].Values["title"]; got != "Beta" {
		t.Fatalf("page row title = %#v", got)
	}
	if got := changeList.Rows[0].Values["published"]; got != BooleanIcon(false) {
		t.Fatalf("boolean display = %#v", got)
	}
	if got := changeList.Rows[0].Values["summary"]; got != "Beta!" {
		t.Fatalf("computed display = %#v", got)
	}
	if changeList.Rows[0].ObjectID != "1" {
		t.Fatalf("object id = %q", changeList.Rows[0].ObjectID)
	}
	if got := changeList.Rows[0].Cells[0].LinkURL; got != "1/change/" {
		t.Fatalf("first cell link = %q", got)
	}
	if got := changeList.Rows[0].Cells[1].LinkURL; got != "" {
		t.Fatalf("editable cell link = %q", got)
	}
	if !changeList.BulkSelection || !changeList.Popup || changeList.PreservedFilters != "status=draft" {
		t.Fatalf("flags/filters = %#v", changeList)
	}
	if !reflect.DeepEqual(changeList.DateHierarchy, []DateBucket{{Year: 2025, Count: 1}, {Year: 2026, Count: 2}}) {
		t.Fatalf("date hierarchy = %#v", changeList.DateHierarchy)
	}
}

func TestChangeListShowAllAndInvalidQuery(t *testing.T) {
	admin := ModelAdmin{ListDisplay: []string{"title"}, ListMaxShowAll: 5}
	rows := []map[string]any{{"title": "A"}, {"title": "B"}}
	changeList, err := BuildChangeList(admin, rows, url.Values{"all": {"1"}})
	if err != nil {
		t.Fatalf("BuildChangeList(show all) error = %v", err)
	}
	if !changeList.ShowAll || len(changeList.Rows) != 2 {
		t.Fatalf("show all change list = %#v", changeList)
	}

	if _, err := BuildChangeList(admin, rows, url.Values{"o": {"missing"}}); !errors.Is(err, ErrInvalidChangeListQuery) {
		t.Fatalf("invalid ordering error = %v, want ErrInvalidChangeListQuery", err)
	}
	if _, err := BuildChangeList(admin, rows, url.Values{"p": {"bad"}}); !errors.Is(err, ErrInvalidChangeListQuery) {
		t.Fatalf("invalid page error = %v, want ErrInvalidChangeListQuery", err)
	}
}

func TestChangeListBulkSelectionRequiresActions(t *testing.T) {
	withoutActions, err := BuildChangeList(ModelAdmin{ListDisplay: []string{"title"}}, []map[string]any{{"id": 1, "title": "A"}}, nil)
	if err != nil {
		t.Fatalf("BuildChangeList(without actions) error = %v", err)
	}
	if withoutActions.BulkSelection {
		t.Fatalf("bulk selection should be disabled without configured actions: %#v", withoutActions)
	}

	withActions, err := BuildChangeList(ModelAdmin{
		ListDisplay:       []string{"title"},
		ActionDefinitions: []Action{{Name: "publish", Label: "Publish"}},
	}, []map[string]any{{"id": 1, "title": "A"}}, nil)
	if err != nil {
		t.Fatalf("BuildChangeList(with actions) error = %v", err)
	}
	if !withActions.BulkSelection {
		t.Fatalf("bulk selection should be enabled with configured actions: %#v", withActions)
	}
}

func TestChangeListHonorsExplicitListDisplayLinks(t *testing.T) {
	admin := ModelAdmin{ListDisplay: []string{"title", "slug"}, ListDisplayLinks: []string{"slug"}}
	changeList, err := BuildChangeList(admin, []map[string]any{{"id": 7, "title": "A", "slug": "a"}}, nil)
	if err != nil {
		t.Fatalf("BuildChangeList() error = %v", err)
	}
	if changeList.Columns[0].Link || !changeList.Columns[1].Link {
		t.Fatalf("column links = %#v", changeList.Columns)
	}
	if got := changeList.Rows[0].Cells[0].LinkURL; got != "" {
		t.Fatalf("title link = %q", got)
	}
	if got := changeList.Rows[0].Cells[1].LinkURL; got != "7/change/" {
		t.Fatalf("slug link = %q", got)
	}
}

func TestChangeListTemplateUsesDjangoBooleanIconsFiltersAndActionMarkup(t *testing.T) {
	changeList, err := BuildChangeList(ModelAdmin{
		Model:             authMetadataByLabel()["auth.User"],
		ListDisplay:       []string{"username", "email", "is_staff"},
		ListDisplayLinks:  []string{"username"},
		ListFilter:        []string{"is_staff", "is_superuser", "is_active"},
		EmptyValueDisplay: "-",
		ActionDefinitions: []Action{DeleteSelectedAction()},
	}, []map[string]any{
		{"id": 1, "username": "admin", "email": "admin@example.com", "is_staff": true},
	}, url.Values{})
	if err != nil {
		t.Fatalf("BuildChangeList() error = %v", err)
	}

	rendered, err := RenderTemplate("change_list.html", adminPageData{
		CSRFToken:              "token",
		AddURL:                 "/admin/auth/user/add/",
		ModelVerboseName:       "user",
		ModelVerboseNamePlural: "users",
		ListFilters:            listFilters(ModelAdmin{ListFilter: []string{"is_staff", "is_superuser", "is_active"}}),
		Actions:                []Action{DeleteSelectedAction()},
		ChangeList:             changeList,
	}, nil)
	if err != nil {
		t.Fatalf("RenderTemplate(change_list) error = %v", err)
	}
	for _, want := range []string{
		`<div class="module filtered" id="changelist">`,
		`<search id="changelist-filter" aria-labelledby="changelist-filter-header">`,
		`<h2 id="changelist-filter-header">Filter</h2>`,
		`data-filter-title="staff status"`,
		`<summary>`,
		`By staff status`,
		`<input type="hidden" name="select_across" value="0" class="select-across">`,
		`<button type="submit" class="button" title="Run the selected action" name="index" value="0">Run</button>`,
		`<th class="action-checkbox-column" scope="col">`,
		`aria-label="Select all objects on this page for an action"`,
		`<th class="sortable column-username`,
		`<div class="text"><a href="?o=1" role="button">Username</a></div>`,
		`<th class="field-username"><a href="1/change/">admin</a></th>`,
		`<td class="field-is_staff"><img src="/admin/static/admin/img/icon-yes.svg" alt="True"></td>`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("change list missing %q:\n%s", want, rendered)
		}
	}
}

func TestChangeListRendersNumericDjangoBooleanFieldsAsIcons(t *testing.T) {
	changeList, err := BuildChangeList(ModelAdmin{
		Model:        authMetadataByLabel()["auth.User"],
		ListDisplay:  []string{"username", "is_staff", "is_active"},
		ListEditable: []string{"is_staff"},
	}, []map[string]any{
		{"id": 1, "username": "admin", "is_staff": int64(1), "is_active": int64(0)},
	}, url.Values{})
	if err != nil {
		t.Fatalf("BuildChangeList() error = %v", err)
	}

	if got := changeList.Rows[0].Values["is_staff"]; got != BooleanIcon(true) {
		t.Fatalf("is_staff display = %#v", got)
	}
	if got := changeList.Rows[0].Values["is_active"]; got != BooleanIcon(false) {
		t.Fatalf("is_active display = %#v", got)
	}
}
