package admin

import (
	"errors"
	"testing"

	"github.com/Newton-School/gogo/auth"
)

func TestDeleteSelectedActionRequiresConfirmationAndDeletes(t *testing.T) {
	action := DeleteSelectedAction()
	store := NewMemoryActionStore()
	rows := []map[string]any{{"id": 1}, {"id": 2}}
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, IsSuperuser: true}}}

	result, err := ExecuteAction(action, ActionContext{User: user, Selected: rows, Store: store})
	if err != nil {
		t.Fatalf("ExecuteAction(unconfirmed) error = %v", err)
	}
	if !result.ConfirmationRequired || len(store.Deleted) != 0 {
		t.Fatalf("unconfirmed result/store = %#v / %#v", result, store.Deleted)
	}

	result, err = ExecuteAction(action, ActionContext{User: user, Selected: rows, Store: store, Confirmed: true})
	if err != nil {
		t.Fatalf("ExecuteAction(confirmed) error = %v", err)
	}
	if result.Message != "Deleted 2 objects" || len(store.Deleted) != 2 {
		t.Fatalf("confirmed result/store = %#v / %#v", result, store.Deleted)
	}
}

func TestCustomActionsRegistryPermissionsAndErrors(t *testing.T) {
	global := NewActionRegistry()
	global.Register(Action{Name: "export", Label: "Export", Handler: func(ctx ActionContext) (ActionResult, error) {
		return ActionResult{Message: "exported"}, nil
	}})
	modelAction := Action{Name: "publish", Label: "Publish", Permissions: []string{"blog.change_post"}, Handler: func(ctx ActionContext) (ActionResult, error) {
		return ActionResult{Message: "published"}, nil
	}}
	admin := ModelAdmin{ActionDefinitions: []Action{modelAction}}

	actions := AvailableActions(global, admin)
	if got := actionNames(actions); len(got) != 2 || got[0] != "export" || got[1] != "publish" {
		t.Fatalf("actions = %#v", got)
	}

	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{
		ID:       1,
		IsActive: true,
		UserPermissions: []auth.Permission{
			{Codename: "change_post", ContentType: auth.ContentType{AppLabel: "blog", Model: "post"}},
		},
	}}}
	result, err := ExecuteAction(modelAction, ActionContext{User: user})
	if err != nil || result.Message != "published" {
		t.Fatalf("ExecuteAction(publish) = %#v, %v", result, err)
	}

	denied := user
	denied.UserPermissions = nil
	if _, err := ExecuteAction(modelAction, ActionContext{User: denied}); !errors.Is(err, ErrAdminPermissionDenied) {
		t.Fatalf("ExecuteAction(denied) error = %v, want ErrAdminPermissionDenied", err)
	}

	wantErr := errors.New("boom")
	failing := Action{Name: "fail", Handler: func(ActionContext) (ActionResult, error) { return ActionResult{}, wantErr }}
	if _, err := ExecuteAction(failing, ActionContext{User: user}); !errors.Is(err, wantErr) {
		t.Fatalf("ExecuteAction(failing) error = %v, want %v", err, wantErr)
	}
}

func actionNames(actions []Action) []string {
	names := make([]string, len(actions))
	for i, action := range actions {
		names[i] = action.Name
	}
	return names
}
