package admin

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/cybersaksham/gogo/auth"
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

type processorStore struct {
	inTransaction bool
	rolledBack    bool
	created       map[string]any
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

func (s *processorStore) Update(context.Context, models.Metadata, string, map[string]any, bool) (map[string]any, error) {
	return nil, nil
}

func (s *processorStore) Delete(context.Context, models.Metadata, string) error {
	return nil
}
