package auth

import (
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/models"
)

type customUser struct {
	AbstractUser
	Timezone string
}

func TestAuthModelsExposeDjangoCompatibleMetadata(t *testing.T) {
	tests := []struct {
		name        string
		meta        models.Metadata
		table       string
		verbose     string
		plural      string
		fieldNames  []string
		permissions []string
	}{
		{
			name:        "permission",
			meta:        Permission{}.ModelMeta(),
			table:       "auth_permission",
			verbose:     "permission",
			plural:      "permissions",
			fieldNames:  []string{"id", "name", "content_type", "codename"},
			permissions: []string{"add", "change", "delete", "view"},
		},
		{
			name:        "group",
			meta:        Group{}.ModelMeta(),
			table:       "auth_group",
			verbose:     "group",
			plural:      "groups",
			fieldNames:  []string{"id", "name", "permissions"},
			permissions: []string{"add", "change", "delete", "view"},
		},
		{
			name:    "user",
			meta:    User{}.ModelMeta(),
			table:   "auth_user",
			verbose: "user",
			plural:  "users",
			fieldNames: []string{
				"id",
				"password",
				"last_login",
				"is_superuser",
				"username",
				"first_name",
				"last_name",
				"email",
				"is_staff",
				"is_active",
				"date_joined",
				"groups",
				"user_permissions",
			},
			permissions: []string{"add", "change", "delete", "view"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.meta.AppLabel != "auth" || tt.meta.TableName != tt.table || tt.meta.DBTable != tt.table {
				t.Fatalf("metadata identity = %#v", tt.meta)
			}
			if tt.meta.VerboseName != tt.verbose || tt.meta.VerboseNamePlural != tt.plural {
				t.Fatalf("verbose names = (%q, %q)", tt.meta.VerboseName, tt.meta.VerboseNamePlural)
			}
			if got := fieldNames(tt.meta.Fields); !reflect.DeepEqual(got, tt.fieldNames) {
				t.Fatalf("fields = %#v, want %#v", got, tt.fieldNames)
			}
			if !reflect.DeepEqual(tt.meta.DefaultPermissions, tt.permissions) {
				t.Fatalf("DefaultPermissions = %#v", tt.meta.DefaultPermissions)
			}
			if tt.meta.DefaultManagerName != "objects" || tt.meta.BaseManagerName != "objects" {
				t.Fatalf("manager names = (%q, %q)", tt.meta.DefaultManagerName, tt.meta.BaseManagerName)
			}
		})
	}
}

func TestAuthRelationshipsUseStableTargets(t *testing.T) {
	groupMeta := Group{}.ModelMeta()
	userMeta := User{}.ModelMeta()

	permissions := fieldByName(groupMeta.Fields, "permissions")
	if permissions.RelationTarget != "auth.Permission" {
		t.Fatalf("group permissions target = %q", permissions.RelationTarget)
	}

	groups := fieldByName(userMeta.Fields, "groups")
	if groups.RelationTarget != "auth.Group" {
		t.Fatalf("user groups target = %q", groups.RelationTarget)
	}

	userPermissions := fieldByName(userMeta.Fields, "user_permissions")
	if userPermissions.RelationTarget != "auth.Permission" {
		t.Fatalf("user permissions target = %q", userPermissions.RelationTarget)
	}
}

func TestModelMetadataIncludesBuiltInAuthModels(t *testing.T) {
	metadata := ModelMetadata()
	got := make([]string, len(metadata))
	for i, meta := range metadata {
		got[i] = meta.Label()
	}
	want := []string{"auth.ContentType", "auth.Permission", "auth.Group", "auth.User"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelMetadata labels = %#v, want %#v", got, want)
	}
}

func TestAbstractUserEmbeddingProvidesExtendableUserShape(t *testing.T) {
	joined := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	user := customUser{
		AbstractUser: AbstractUser{
			AbstractBaseUser: AbstractBaseUser{
				ID:            7,
				Password:      "hash",
				IsSuperuser:   true,
				LastLogin:     joined,
				IsActive:      true,
				DateJoined:    joined,
				Groups:        []Group{{ID: 1, Name: "staff"}},
				Permissions:   []Permission{{ID: 2, Codename: "view_user"}},
				Anonymous:     false,
				Authenticated: true,
			},
			Username:  "saksham",
			FirstName: "Saksham",
			LastName:  "Singh",
			Email:     "saksham@example.com",
			IsStaff:   true,
		},
		Timezone: "Asia/Kolkata",
	}

	if user.ID != 7 || user.Username != "saksham" || user.Timezone != "Asia/Kolkata" {
		t.Fatalf("embedded fields not promoted: %#v", user)
	}
	if !user.IsAuthenticated() || user.IsAnonymous() {
		t.Fatalf("authentication flags = authenticated:%v anonymous:%v", user.IsAuthenticated(), user.IsAnonymous())
	}
	if len(user.Groups) != 1 || len(user.Permissions) != 1 {
		t.Fatalf("embedded relationships = %#v / %#v", user.Groups, user.Permissions)
	}
}

func fieldNames(fields []models.FieldMeta) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = field.Name
	}
	return names
}

func fieldByName(fields []models.FieldMeta, name string) models.FieldMeta {
	for _, field := range fields {
		if field.Name == name {
			return field
		}
	}
	return models.FieldMeta{}
}
