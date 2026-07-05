package admin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/files"
	"github.com/cybersaksham/gogo/forms"
	"github.com/cybersaksham/gogo/models"
)

func TestAdminFormProcessorRunsAddLifecycleHooksTransactionAndResponse(t *testing.T) {
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
			{Name: "slug", Column: "slug"},
		},
	}
	store := &processorStore{}
	site := DefaultSite()
	site.ModelStore = store
	site.LogStore = NewMemoryLogStore()
	var calls []string
	var message string
	admin := ModelAdmin{
		Model:  meta,
		Fields: []string{"title"},
		Hooks: ModelAdminHooks{
			SaveForm: func(_ *http.Request, object any) (any, error) {
				calls = append(calls, "save_form")
				values := object.(map[string]any)
				values["slug"] = "new-post"
				return values, nil
			},
			SaveModel: func(_ *http.Request, object any) error {
				calls = append(calls, "save_model")
				if object.(map[string]any)["id"] != 1 {
					t.Fatalf("saved object = %#v", object)
				}
				return nil
			},
			SaveRelated: func(_ *http.Request, object any) error {
				calls = append(calls, "save_related")
				return nil
			},
			MessageUser: func(_ *http.Request, text string) {
				calls = append(calls, "message_user")
				message = text
			},
			ResponseAdd: func(_ *http.Request, object any) http.Handler {
				calls = append(calls, "response_add")
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Location", "/custom/")
					w.WriteHeader(http.StatusSeeOther)
				})
			},
		},
	}
	request := staffAdminFormRequest("/admin/blog/post/add/", "title=New")
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 9, IsActive: true, Authenticated: true}, IsStaff: true}}

	response, err := AdminFormProcessor{Site: site, ModelAdmin: admin, Mode: ChangeFormAdd}.Process(context.Background(), AdminFormProcessInput{
		Request: request,
		User:    user,
		Values:  map[string]any{"title": "New"},
	})
	if err != nil {
		t.Fatalf("Process(add) error = %v", err)
	}
	if response.Status() != http.StatusSeeOther || response.Header().Get("Location") != "/custom/" {
		t.Fatalf("response = %d location=%q", response.Status(), response.Header().Get("Location"))
	}
	if !store.inTransaction || !reflect.DeepEqual(store.created, map[string]any{"title": "New", "slug": "new-post"}) {
		t.Fatalf("store transaction=%v created=%#v", store.inTransaction, store.created)
	}
	if !reflect.DeepEqual(calls, []string{"save_form", "save_model", "save_related", "message_user", "response_add"}) {
		t.Fatalf("hook calls = %#v", calls)
	}
	if message != "Added post \"New\"." {
		t.Fatalf("message = %q", message)
	}
	entries, err := site.LogStore.EntriesForObject(meta.Label(), "1")
	if err != nil || len(entries) != 1 || entries[0].ActionFlag != ActionFlagAddition || entries[0].UserID != 9 {
		t.Fatalf("log entries = %#v err=%v", entries, err)
	}
}

func TestAdminFormProcessorRollsBackOnHookError(t *testing.T) {
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
		},
	}
	store := &processorStore{}
	site := DefaultSite()
	site.ModelStore = store
	admin := ModelAdmin{
		Model:  meta,
		Fields: []string{"title"},
		Hooks:  ModelAdminHooks{SaveModel: func(*http.Request, any) error { return errors.New("stop") }},
	}
	_, err := AdminFormProcessor{Site: site, ModelAdmin: admin, Mode: ChangeFormAdd}.Process(context.Background(), AdminFormProcessInput{
		Request: staffAdminFormRequest("/admin/blog/post/add/", "title=New"),
		User:    auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 2, IsActive: true, Authenticated: true}, IsStaff: true}},
		Values:  map[string]any{"title": "New"},
	})
	if err == nil || !store.rolledBack {
		t.Fatalf("error=%v rolledBack=%v", err, store.rolledBack)
	}
}

func TestAdminFormProcessorSavesManyToManyAndFiles(t *testing.T) {
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
			{Name: "attachment", Column: "attachment", Kind: "file", Blank: true, UploadTo: "uploads"},
			{Name: "tags", RelationTarget: "blog.Tag", RelationType: "many_to_many", Blank: true},
		},
	}
	store := &processorStore{}
	storage := files.NewLocalStorage(t.TempDir(), files.LocalOptions{})
	site := DefaultSite()
	site.ModelStore = store
	site.FileStorage = storage
	admin := ModelAdmin{Model: meta, Fields: []string{"title", "attachment", "tags"}}
	upload := forms.UploadedFile{Name: "doc.txt", Content: []byte("hello"), Size: 5, ContentType: "text/plain"}
	ctx := context.Background()
	_, err := AdminFormProcessor{Site: site, ModelAdmin: admin, Mode: ChangeFormAdd}.Process(ctx, AdminFormProcessInput{
		Request: staffAdminFormRequest("/admin/blog/post/add/", "title=New"),
		User:    auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 2, IsActive: true, Authenticated: true}, IsStaff: true}},
		Values:  map[string]any{"title": "New", "attachment": upload, "tags": []string{"1", "2"}},
	})
	if err != nil {
		t.Fatalf("Process(add m2m/file) error = %v", err)
	}
	if _, ok := store.created["tags"]; ok {
		t.Fatalf("many-to-many field should not be sent to Create: %#v", store.created)
	}
	if store.created["attachment"] != "uploads/doc.txt" {
		t.Fatalf("created attachment = %#v", store.created["attachment"])
	}
	reader, err := storage.Open(ctx, "uploads/doc.txt")
	if err != nil {
		t.Fatalf("Open(uploaded file) error = %v", err)
	}
	content, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(content) != "hello" {
		t.Fatalf("stored upload content = %q err=%v", content, err)
	}
	if !reflect.DeepEqual(store.m2m, []processorM2M{{objectID: "1", field: "tags", values: []string{"1", "2"}}}) {
		t.Fatalf("m2m saves = %#v", store.m2m)
	}
}

func TestAdminFormProcessorClearsExistingFile(t *testing.T) {
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
			{Name: "attachment", Column: "attachment", Kind: "file", Blank: true},
		},
	}
	store := &processorStore{}
	site := DefaultSite()
	site.ModelStore = store
	admin := ModelAdmin{Model: meta, Fields: []string{"title", "attachment"}}
	_, err := AdminFormProcessor{Site: site, ModelAdmin: admin, Mode: ChangeFormEdit}.Process(context.Background(), AdminFormProcessInput{
		Request:  staffAdminFormRequest("/admin/blog/post/1/change/", "title=New&attachment-clear=on"),
		User:     auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 2, IsActive: true, Authenticated: true}, IsStaff: true}},
		ObjectID: "1",
		Existing: map[string]any{"id": 1, "title": "Old", "attachment": "old.pdf"},
		Values:   map[string]any{"title": "New", "attachment-clear": "on"},
	})
	if err != nil {
		t.Fatalf("Process(change clear file) error = %v", err)
	}
	if store.updated["attachment"] != "" {
		t.Fatalf("updated attachment = %#v, want cleared empty string", store.updated["attachment"])
	}
}

func TestAdminFormValuesParsesMultipartUploads(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("title", "Multipart"); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	part, err := writer.CreateFormFile("attachment", "doc.txt")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := io.WriteString(part, "hello"); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/blog/post/add/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())

	values := formValues(request)
	if values["title"] != "Multipart" {
		t.Fatalf("title = %#v", values["title"])
	}
	file, ok := values["attachment"].(forms.UploadedFile)
	if !ok || file.Name != "doc.txt" || string(file.Content) != "hello" || file.Size != 5 {
		t.Fatalf("attachment = %#v", values["attachment"])
	}
}

type processorStore struct {
	inTransaction bool
	rolledBack    bool
	created       map[string]any
	updated       map[string]any
	m2m           []processorM2M
}

type processorM2M struct {
	objectID string
	field    string
	values   []string
}

func (s *processorStore) Atomic(ctx context.Context, fn func(context.Context) error) error {
	s.inTransaction = true
	err := fn(ctx)
	if err != nil {
		s.rolledBack = true
	}
	return err
}

func (s *processorStore) List(context.Context, models.Metadata) ([]map[string]any, error) {
	return nil, nil
}

func (s *processorStore) Get(context.Context, models.Metadata, string) (map[string]any, bool, error) {
	return nil, false, nil
}

func (s *processorStore) Create(_ context.Context, _ models.Metadata, values map[string]any) (map[string]any, error) {
	s.created = cloneRow(values)
	row := cloneRow(values)
	row["id"] = 1
	return row, nil
}

func (s *processorStore) Update(_ context.Context, _ models.Metadata, objectID string, values map[string]any, _ bool) (map[string]any, error) {
	s.updated = cloneRow(values)
	row := cloneRow(values)
	row["id"] = objectID
	return row, nil
}

func (s *processorStore) Delete(context.Context, models.Metadata, string) error {
	return nil
}

func (s *processorStore) SetManyToMany(_ context.Context, _ models.Metadata, objectID string, field string, values []string) error {
	s.m2m = append(s.m2m, processorM2M{objectID: objectID, field: field, values: append([]string(nil), values...)})
	return nil
}
