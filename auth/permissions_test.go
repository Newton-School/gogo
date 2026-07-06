package auth

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/models"
)

func TestGeneratePermissionsIncludesDefaultAndCustomModelPermissions(t *testing.T) {
	registry := models.NewRegistry()
	if err := registry.RegisterMetadata(models.Metadata{
		AppLabel:    "blog",
		ModelName:   "Post",
		TableName:   "blog_post",
		VerboseName: "post",
		Permissions: []models.Permission{
			{CodeName: "publish_post", Name: "Can publish post"},
		},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	permissions, err := GenerateModelPermissions(registry)
	if err != nil {
		t.Fatalf("GenerateModelPermissions() error = %v", err)
	}

	got := permissionKeys(permissions)
	want := []string{"blog.add_post", "blog.change_post", "blog.delete_post", "blog.view_post", "blog.publish_post"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %#v, want %#v", got, want)
	}
	if permissions[0].Name != "Can add post" || permissions[0].ContentTypeID == 0 {
		t.Fatalf("default permission metadata = %#v", permissions[0])
	}
	if permissions[4].Name != "Can publish post" {
		t.Fatalf("custom permission metadata = %#v", permissions[4])
	}
}

func TestPermissionChecksCoverDirectGroupSuperuserInactiveAndModules(t *testing.T) {
	viewPost := Permission{Codename: "view_post", ContentType: ContentType{AppLabel: "blog", Model: "post"}}
	changePost := Permission{Codename: "change_post", ContentType: ContentType{AppLabel: "blog", Model: "post"}}
	viewOrder := Permission{Codename: "view_order", ContentType: ContentType{AppLabel: "shop", Model: "order"}}

	user := User{AbstractUser: AbstractUser{AbstractBaseUser: AbstractBaseUser{
		ID:              10,
		IsActive:        true,
		UserPermissions: []Permission{viewPost},
		Groups:          []Group{{Name: "editors", Permissions: []Permission{changePost}}},
	}}}

	if !HasPerm(user, "blog.view_post") {
		t.Fatalf("direct permission check failed")
	}
	if !HasPerm(user, "blog.change_post") {
		t.Fatalf("group permission check failed")
	}
	if HasPerm(user, "shop.view_order") {
		t.Fatalf("unexpected shop permission")
	}
	if !HasModulePerms(user, "blog") || HasModulePerms(user, "shop") {
		t.Fatalf("module permissions mismatch")
	}
	if got := GetUserPermissions(user); !reflect.DeepEqual(got, []string{"blog.view_post"}) {
		t.Fatalf("user permissions = %#v", got)
	}
	if got := GetGroupPermissions(user); !reflect.DeepEqual(got, []string{"blog.change_post"}) {
		t.Fatalf("group permissions = %#v", got)
	}
	if got := GetAllPermissions(user); !reflect.DeepEqual(got, []string{"blog.change_post", "blog.view_post"}) {
		t.Fatalf("all permissions = %#v", got)
	}

	superuser := User{AbstractUser: AbstractUser{AbstractBaseUser: AbstractBaseUser{ID: 1, IsActive: true, IsSuperuser: true}}}
	if !HasPerm(superuser, "anything.really") {
		t.Fatalf("active superuser should pass every permission")
	}

	inactive := user
	inactive.IsActive = false
	if HasPerm(inactive, "blog.view_post") || HasModulePerms(inactive, "blog") {
		t.Fatalf("inactive user should fail permission checks")
	}

	filtered := WithPerm([]User{user, inactive, superuser, {AbstractUser: AbstractUser{AbstractBaseUser: AbstractBaseUser{ID: 99, IsActive: true, UserPermissions: []Permission{viewOrder}}}}}, "blog.view_post")
	if len(filtered) != 2 || filtered[0].ID != user.ID || filtered[1].ID != superuser.ID {
		t.Fatalf("WithPerm result = %#v", filtered)
	}
}

func permissionKeys(permissions []Permission) []string {
	keys := make([]string, len(permissions))
	for i, permission := range permissions {
		keys[i] = permission.Key()
	}
	return keys
}
