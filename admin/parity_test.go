package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/admin/parity"
	"github.com/Newton-School/gogo/models"
)

type parityPage string

const (
	parityPageIndex          parityPage = "index"
	parityPageAppIndex       parityPage = "app_index"
	parityPageChangeList     parityPage = "change_list"
	parityPageAdd            parityPage = "add"
	parityPageChange         parityPage = "change"
	parityPageDelete         parityPage = "delete"
	parityPageDeleteSelected parityPage = "delete_selected"
	parityPageHistory        parityPage = "history"
	parityPageLogin          parityPage = "login"
	parityPagePopup          parityPage = "popup_response"
	parityPageReadonly       parityPage = "readonly_detail"
	parityPageAutocomplete   parityPage = "autocomplete"
)

func TestAdminParityDOMSnapshots(t *testing.T) {
	for _, page := range []parityPage{
		parityPageIndex,
		parityPageAppIndex,
		parityPageChangeList,
		parityPageAdd,
		parityPageChange,
		parityPageDelete,
		parityPageDeleteSelected,
		parityPageHistory,
		parityPageLogin,
		parityPagePopup,
		parityPageReadonly,
	} {
		t.Run(string(page), func(t *testing.T) {
			body := renderAdminParityPage(t, page)
			parity.AssertGolden(t, string(page)+".html", parity.NormalizeHTML(body))
		})
	}
}

func TestAdminParityChangeListDOM(t *testing.T) {
	html := parity.NormalizeHTML(renderAdminParityPage(t, parityPageChangeList))
	parity.AssertContains(t, html,
		`<body class="app-blog model-post change-list"`,
		`id="changelist-form"`,
		`name="action"`,
		`name="_selected_action"`,
		`class="paginator"`,
	)
}

func TestAdminParityAutocompleteJSON(t *testing.T) {
	body := renderAdminParityPage(t, parityPageAutocomplete)
	var payload struct {
		Results []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"results"`
		Pagination struct {
			More bool `json:"more"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("autocomplete JSON did not decode: %v\n%s", err, body)
	}
	if len(payload.Results) != 1 || payload.Results[0].ID != "1" || payload.Results[0].Text != "First parity post" {
		t.Fatalf("autocomplete payload = %#v", payload)
	}
}

func TestAdminParityDjangoReferenceOptional(t *testing.T) {
	parity.RunOptionalDjangoReference(t)
}

func renderAdminParityPage(t *testing.T, page parityPage) string {
	t.Helper()
	site, router := newParityAdminSite(t)
	path := "/admin/"
	method := http.MethodGet
	body := ""
	switch page {
	case parityPageIndex:
		path = "/admin/"
	case parityPageAppIndex:
		path = "/admin/blog/"
	case parityPageChangeList:
		path = "/admin/blog/post/"
	case parityPageAdd:
		path = "/admin/blog/post/add/"
	case parityPageChange, parityPageReadonly:
		path = "/admin/blog/post/1/change/"
	case parityPageDelete:
		path = "/admin/blog/post/1/delete/"
	case parityPageDeleteSelected:
		method = http.MethodPost
		path = "/admin/blog/post/"
		body = "action=delete_selected&_selected_action=1&index=0"
	case parityPageHistory:
		path = "/admin/blog/post/1/history/"
	case parityPageLogin:
		request := httptest.NewRequest(http.MethodGet, "/admin/login/?next=/admin/", nil)
		response := httptest.NewRecorder()
		site.LoginView.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("login status = %d body=%s", response.Code, response.Body.String())
		}
		return response.Body.String()
	case parityPagePopup:
		rendered, err := RenderTemplate("popup_response.html", map[string]any{"PopupResponseData": `{"value":"1","obj":"First parity post"}`}, nil)
		if err != nil {
			t.Fatalf("popup template error = %v", err)
		}
		return rendered
	case parityPageAutocomplete:
		path = "/admin/blog/post/autocomplete/?q=First"
	default:
		t.Fatalf("unsupported parity page %q", page)
	}

	var request *http.Request
	if method == http.MethodPost {
		request = staffAdminFormRequest(path, body)
	} else {
		request = staffAdminRequest(method, path)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d body=%s", method, path, response.Code, response.Body.String())
	}
	return response.Body.String()
}

func newParityAdminSite(t *testing.T) (*Site, http.Handler) {
	t.Helper()
	meta := models.Metadata{
		AppLabel:          "blog",
		ModelName:         "Post",
		TableName:         "blog_post",
		VerboseName:       "post",
		VerboseNamePlural: "posts",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title", Kind: "text"},
			{Name: "status", Column: "status", Kind: "text"},
		},
	}
	site := DefaultSite()
	site.LoginView = LoginView(AuthViewConfig{Site: site})
	site.ModelStore = &parityMemoryStore{
		rows: []map[string]any{{"id": 1, "title": "First parity post", "status": "draft"}},
	}
	site.LogStore = NewMemoryLogStore()
	site.LogStore.(*MemoryLogStore).Now = func() time.Time {
		return time.Date(2026, 7, 5, 8, 0, 0, 0, time.UTC)
	}
	if err := site.LogStore.Log(AdminLogEntry{
		UserID:        1,
		ContentType:   meta.Label(),
		ObjectID:      "1",
		ObjectRepr:    "First parity post",
		ActionFlag:    ActionFlagChange,
		ChangeMessage: "Changed title",
	}); err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{
		ListDisplay:      []string{"title", "status"},
		ListDisplayLinks: []string{"title"},
		ListFilter:       []string{"status"},
		SearchFields:     []string{"title"},
		Fields:           []string{"title", "status"},
		ReadonlyFields:   []string{"id"},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}
	return site, router
}

type parityMemoryStore struct {
	rows []map[string]any
}

func (s *parityMemoryStore) List(context.Context, models.Metadata) ([]map[string]any, error) {
	return cloneRows(s.rows), nil
}

func (s *parityMemoryStore) Get(_ context.Context, _ models.Metadata, objectID string) (map[string]any, bool, error) {
	for _, row := range s.rows {
		if strings.TrimSpace(objectID) == fmt.Sprint(row["id"]) {
			return cloneRow(row), true, nil
		}
	}
	return nil, false, nil
}

func (s *parityMemoryStore) Create(_ context.Context, _ models.Metadata, values map[string]any) (map[string]any, error) {
	row := cloneRow(values)
	if row["id"] == nil {
		row["id"] = len(s.rows) + 1
	}
	s.rows = append(s.rows, cloneRow(row))
	return row, nil
}

func (s *parityMemoryStore) Update(_ context.Context, _ models.Metadata, objectID string, values map[string]any, _ bool) (map[string]any, error) {
	for index, row := range s.rows {
		if fmt.Sprint(row["id"]) != strings.TrimSpace(objectID) {
			continue
		}
		updated := cloneRow(row)
		for key, value := range values {
			updated[key] = value
		}
		s.rows[index] = cloneRow(updated)
		return updated, nil
	}
	return nil, fmt.Errorf("admin parity object %q not found", objectID)
}

func (s *parityMemoryStore) Delete(_ context.Context, _ models.Metadata, objectID string) error {
	for index, row := range s.rows {
		if fmt.Sprint(row["id"]) == strings.TrimSpace(objectID) {
			s.rows = append(s.rows[:index], s.rows[index+1:]...)
			return nil
		}
	}
	return nil
}
