package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Newton-School/gogo/auth"
)

func TestInlineFormsetsBuildMetadataAndFilterPermissions(t *testing.T) {
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, Authenticated: true}}}
	request := httptest.NewRequest("GET", "/admin/blog/post/1/change/", nil)
	inlines := []Inline{
		{Model: "blog.Comment", Kind: InlineTabular, Extra: 2, MinNum: 1, MaxNum: 3, CanDelete: true, ShowChangeLink: true, FKName: "post_id", HasPermission: func(*http.Request, auth.User) bool { return true }},
		{Model: "blog.Secret", Kind: InlineStacked, HasPermission: func(*http.Request, auth.User) bool { return false }},
	}
	rows := map[string][]map[string]any{
		"blog.Comment": {{"id": 1, "body": "First"}, {"id": 2, "body": "Second"}},
		"blog.Secret":  {{"id": 10, "body": "Hidden"}},
	}

	formsets := BuildInlineFormsets(inlines, InlineInput{ParentID: "99", User: user, Request: request, Rows: rows})
	if len(formsets) != 1 {
		t.Fatalf("formsets = %#v", formsets)
	}
	formset := formsets[0]
	if formset.Model != "blog.Comment" || formset.Kind != InlineTabular || formset.ExtraForms != 2 || formset.FKName != "post_id" {
		t.Fatalf("formset metadata = %#v", formset)
	}
	if !formset.CanDelete || !formset.ShowChangeLink || len(formset.Forms) != 2 {
		t.Fatalf("formset flags/forms = %#v", formset)
	}
}

func TestInlineValidationSavingAndDeletion(t *testing.T) {
	formset := InlineFormset{
		Model:     "blog.Comment",
		MinNum:    1,
		MaxNum:    2,
		CanDelete: true,
		Forms: []InlineForm{
			{Values: map[string]any{"id": 1, "body": "Updated"}},
			{Values: map[string]any{"id": 2, "body": "Delete"}, Delete: true},
		},
	}
	if err := ValidateInlineFormset(formset); err != nil {
		t.Fatalf("ValidateInlineFormset(valid) error = %v", err)
	}
	store := NewMemoryInlineStore()
	if err := SaveInlineFormset(formset, store); err != nil {
		t.Fatalf("SaveInlineFormset() error = %v", err)
	}
	if len(store.Saved) != 1 || store.Saved[0]["body"] != "Updated" {
		t.Fatalf("saved rows = %#v", store.Saved)
	}
	if len(store.Deleted) != 1 || store.Deleted[0]["id"] != 2 {
		t.Fatalf("deleted rows = %#v", store.Deleted)
	}

	tooFew := formset
	tooFew.Forms = nil
	if err := ValidateInlineFormset(tooFew); !errors.Is(err, ErrInvalidInlineFormset) {
		t.Fatalf("tooFew error = %v, want ErrInvalidInlineFormset", err)
	}
	tooMany := formset
	tooMany.Forms = append(tooMany.Forms, InlineForm{Values: map[string]any{"id": 3}})
	if err := ValidateInlineFormset(tooMany); !errors.Is(err, ErrInvalidInlineFormset) {
		t.Fatalf("tooMany error = %v, want ErrInvalidInlineFormset", err)
	}
}
