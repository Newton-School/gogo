package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/models"
)

func TestAdminInlineFormsetsParseValidateAndSave(t *testing.T) {
	parentMeta, childMeta := inlineTestMetas()
	store := &inlineModelStore{rows: map[string][]map[string]any{
		parentMeta.Label(): {{"id": "1", "title": "Parent"}},
		childMeta.Label(): {
			{"id": "10", "post_id": "1", "body": "Old"},
			{"id": "11", "post_id": "1", "body": "Remove"},
		},
	}}
	site := DefaultSite()
	site.ModelStore = store
	if err := site.ModelRegistry.RegisterMetadata(parentMeta, ModelAdmin{
		Fields:  []string{"title"},
		Inlines: []Inline{{Model: childMeta.Label(), Kind: InlineTabular, CanDelete: true}},
	}); err != nil {
		t.Fatalf("RegisterMetadata(parent) error = %v", err)
	}
	if err := site.ModelRegistry.RegisterMetadata(childMeta, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata(child) error = %v", err)
	}
	parentAdmin, _ := site.ModelRegistry.GetAdmin(parentMeta.Label())
	request := staffAdminFormRequest("/admin/blog/post/1/change/", "title=Parent")
	user, _ := auth.UserFromContext(request.Context())
	values := map[string]any{
		"comment_set-TOTAL_FORMS":   "3",
		"comment_set-INITIAL_FORMS": "2",
		"comment_set-0-id":          "10",
		"comment_set-0-body":        "Updated",
		"comment_set-1-id":          "11",
		"comment_set-1-body":        "Remove",
		"comment_set-1-DELETE":      "on",
		"comment_set-2-body":        "Created",
	}

	formsets, err := BuildAdminInlineFormsets(context.Background(), site, parentAdmin, AdminInlineFormsetInput{
		ParentID:  "1",
		User:      user,
		Request:   request,
		Values:    values,
		Submitted: true,
	})
	if err != nil {
		t.Fatalf("BuildAdminInlineFormsets() error = %v", err)
	}
	formsets, err = ValidateAdminInlineFormsets(context.Background(), request, formsets)
	if err != nil {
		t.Fatalf("ValidateAdminInlineFormsets() error = %v formsets=%#v", err, formsets)
	}
	if err := SaveAdminInlineFormsets(context.Background(), site, request, parentAdmin, "1", formsets); err != nil {
		t.Fatalf("SaveAdminInlineFormsets() error = %v", err)
	}

	if len(store.updated) != 1 || store.updated[0].model != childMeta.Label() || store.updated[0].pk != "10" || store.updated[0].values["body"] != "Updated" {
		t.Fatalf("updated rows = %#v", store.updated)
	}
	if len(store.deleted) != 1 || store.deleted[0].model != childMeta.Label() || store.deleted[0].pk != "11" {
		t.Fatalf("deleted rows = %#v", store.deleted)
	}
	if len(store.created) != 1 || store.created[0].model != childMeta.Label() || store.created[0].values["body"] != "Created" || store.created[0].values["post_id"] != "1" {
		t.Fatalf("created rows = %#v", store.created)
	}
}

func TestAdminChangeFormRendersInlineFormset(t *testing.T) {
	parentMeta, childMeta := inlineTestMetas()
	store := &inlineModelStore{rows: map[string][]map[string]any{
		parentMeta.Label(): {{"id": "1", "title": "Parent"}},
		childMeta.Label():  {{"id": "10", "post_id": "1", "body": "Existing"}},
	}}
	site := DefaultSite()
	site.ModelStore = store
	if err := site.ModelRegistry.RegisterMetadata(parentMeta, ModelAdmin{
		Fields:  []string{"title"},
		Inlines: []Inline{{Model: childMeta.Label(), Kind: InlineTabular, Extra: 1, CanDelete: true}},
	}); err != nil {
		t.Fatalf("RegisterMetadata(parent) error = %v", err)
	}
	if err := site.ModelRegistry.RegisterMetadata(childMeta, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata(child) error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, staffAdminRequest(http.MethodGet, "/admin/blog/post/1/change/"))
	if response.Code != http.StatusOK {
		t.Fatalf("change form status = %d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		`id="comment_set-group"`,
		`data-inline-type="tabular"`,
		`name="comment_set-TOTAL_FORMS" value="2"`,
		`name="comment_set-0-id" value="10"`,
		`name="comment_set-0-body"`,
		`Existing`,
		`name="comment_set-0-DELETE"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("change form missing %q:\n%s", want, body)
		}
	}
	if got := store.query.Filters["post_id__exact"]; !store.queried || !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("inline query = %#v queried=%v", store.query, store.queried)
	}
}

func TestAdminChangeFormRendersInlineValidationErrorsWithoutSaving(t *testing.T) {
	parentMeta, childMeta := inlineTestMetas()
	store := &inlineModelStore{rows: map[string][]map[string]any{
		parentMeta.Label(): {{"id": "1", "title": "Parent"}},
		childMeta.Label():  {{"id": "10", "post_id": "1", "body": "Existing"}},
	}}
	site := DefaultSite()
	site.ModelStore = store
	if err := site.ModelRegistry.RegisterMetadata(parentMeta, ModelAdmin{
		Fields:  []string{"title"},
		Inlines: []Inline{{Model: childMeta.Label(), Kind: InlineStacked, Extra: 0, CanDelete: true}},
	}); err != nil {
		t.Fatalf("RegisterMetadata(parent) error = %v", err)
	}
	if err := site.ModelRegistry.RegisterMetadata(childMeta, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata(child) error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}
	request := staffAdminFormRequest("/admin/blog/post/1/change/", strings.Join([]string{
		"title=Parent",
		"comment_set-TOTAL_FORMS=1",
		"comment_set-INITIAL_FORMS=1",
		"comment_set-0-id=10",
		"comment_set-0-body=",
		"_save=Save",
	}, "&"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("invalid inline status = %d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "This field is required.") {
		t.Fatalf("invalid inline body missing validation error:\n%s", response.Body.String())
	}
	if len(store.created) != 0 || len(store.updated) != 0 || len(store.deleted) != 0 {
		t.Fatalf("store operations after invalid inline = created %#v updated %#v deleted %#v", store.created, store.updated, store.deleted)
	}
}

func inlineTestMetas() (models.Metadata, models.Metadata) {
	parent := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
		},
	}
	child := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Comment",
		TableName: "blog_comment",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "post_id", Column: "post_id", RelationTarget: parent.Label(), RelationType: "foreign_key"},
			{Name: "body", Column: "body"},
		},
	}
	return parent, child
}

type inlineModelStore struct {
	rows    map[string][]map[string]any
	query   models.ObjectQuery
	queried bool
	created []inlineStoreOperation
	updated []inlineStoreOperation
	deleted []inlineStoreOperation
}

type inlineStoreOperation struct {
	model  string
	pk     string
	values map[string]any
}

func (s *inlineModelStore) List(_ context.Context, meta models.Metadata) ([]map[string]any, error) {
	return cloneRows(s.rows[meta.Label()]), nil
}

func (s *inlineModelStore) Query(_ context.Context, meta models.Metadata, query models.ObjectQuery) (models.ObjectQueryResult, error) {
	s.queried = true
	s.query = query
	rows := cloneRows(s.rows[meta.Label()])
	for key, values := range query.Filters {
		if len(values) == 0 || !strings.HasSuffix(key, "__exact") {
			continue
		}
		field := strings.TrimSuffix(key, "__exact")
		filtered := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if fmt.Sprint(row[field]) == values[0] {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	return models.ObjectQueryResult{Rows: rows, Total: len(rows)}, nil
}

func (s *inlineModelStore) Get(_ context.Context, meta models.Metadata, pk string) (map[string]any, bool, error) {
	for _, row := range s.rows[meta.Label()] {
		if fmt.Sprint(row[primaryKeyName(meta)]) == pk {
			return cloneRow(row), true, nil
		}
	}
	return nil, false, nil
}

func (s *inlineModelStore) Create(_ context.Context, meta models.Metadata, values map[string]any) (map[string]any, error) {
	created := cloneRow(values)
	created["id"] = fmt.Sprintf("%d", len(s.rows[meta.Label()])+1)
	s.created = append(s.created, inlineStoreOperation{model: meta.Label(), values: cloneRow(values)})
	s.rows[meta.Label()] = append(s.rows[meta.Label()], created)
	return created, nil
}

func (s *inlineModelStore) Update(_ context.Context, meta models.Metadata, pk string, values map[string]any, _ bool) (map[string]any, error) {
	s.updated = append(s.updated, inlineStoreOperation{model: meta.Label(), pk: pk, values: cloneRow(values)})
	row := cloneRow(values)
	row[primaryKeyName(meta)] = pk
	return row, nil
}

func (s *inlineModelStore) Delete(_ context.Context, meta models.Metadata, pk string) error {
	s.deleted = append(s.deleted, inlineStoreOperation{model: meta.Label(), pk: pk})
	return nil
}
