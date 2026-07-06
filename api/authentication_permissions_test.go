package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

func TestAPIAuthenticationSupportsSessionAndToken(t *testing.T) {
	meta := Token{}.ModelMeta()
	if meta.TableName != "api_token" || apiFieldByName(meta.Fields, "user").RelationTarget != "auth.User" {
		t.Fatalf("token metadata = %#v", meta)
	}

	sessionUser := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, Authenticated: true}}}
	raw := httptest.NewRequest(http.MethodGet, "/api/posts/", nil)
	raw = raw.WithContext(auth.ContextWithUser(raw.Context(), sessionUser))
	sessionRequest := NewRequest(raw)

	if err := AuthenticateRequest(SessionAuthentication())(context.Background(), sessionRequest); err != nil {
		t.Fatalf("session AuthenticateRequest() error = %v", err)
	}
	if sessionRequest.User().ID != 1 || !sessionRequest.User().IsAuthenticated() {
		t.Fatalf("session user = %#v", sessionRequest.User())
	}

	tokenUser := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 2, IsActive: true}}}
	store := NewMemoryTokenStore(Token{Key: "secret", User: tokenUser, CreatedAt: time.Unix(1, 0).UTC()})
	tokenRaw := httptest.NewRequest(http.MethodGet, "/api/posts/", nil)
	tokenRaw.Header.Set("Authorization", "Token secret")
	tokenRequest := NewRequest(tokenRaw)

	if err := AuthenticateRequest(TokenAuthentication(store))(context.Background(), tokenRequest); err != nil {
		t.Fatalf("token AuthenticateRequest() error = %v", err)
	}
	if tokenRequest.User().ID != 2 || !tokenRequest.User().IsAuthenticated() {
		t.Fatalf("token user = %#v", tokenRequest.User())
	}
	if tokenRequest.Auth().(Token).Key != "secret" {
		t.Fatalf("auth metadata = %#v", tokenRequest.Auth())
	}

	badRaw := httptest.NewRequest(http.MethodGet, "/api/posts/", nil)
	badRaw.Header.Set("Authorization", "Token missing")
	err := AuthenticateRequest(TokenAuthentication(store))(context.Background(), NewRequest(badRaw))
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("bad token error = %v, want ErrAuthenticationFailed", err)
	}
}

func TestAPIPermissionsCoverDenialsSafeMethodsModelPermissionsAndObjects(t *testing.T) {
	anonymous := NewRequest(httptest.NewRequest(http.MethodGet, "/api/posts/", nil))
	if err := CheckPermissions(IsAuthenticated())(context.Background(), anonymous); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("anonymous permission error = %v, want ErrPermissionDenied", err)
	}

	readOnly := NewRequest(httptest.NewRequest(http.MethodGet, "/api/posts/", nil))
	if err := CheckPermissions(IsAuthenticatedOrReadOnly())(context.Background(), readOnly); err != nil {
		t.Fatalf("safe read-only permission error = %v", err)
	}
	unsafe := NewRequest(httptest.NewRequest(http.MethodPost, "/api/posts/", nil))
	if err := CheckPermissions(IsAuthenticatedOrReadOnly())(context.Background(), unsafe); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("unsafe read-only permission error = %v, want ErrPermissionDenied", err)
	}

	viewPermission := auth.Permission{Codename: "view_post", ContentType: auth.ContentType{AppLabel: "blog", Model: "post"}}
	addPermission := auth.Permission{Codename: "add_post", ContentType: auth.ContentType{AppLabel: "blog", Model: "post"}}
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{
		ID:              5,
		IsActive:        true,
		Authenticated:   true,
		UserPermissions: []auth.Permission{viewPermission, addPermission},
	}}}

	modelRead := NewRequest(httptest.NewRequest(http.MethodGet, "/api/posts/", nil)).WithUser(user)
	if err := CheckPermissions(ModelPermissions("blog", "post"))(context.Background(), modelRead); err != nil {
		t.Fatalf("model read permission error = %v", err)
	}
	modelWrite := NewRequest(httptest.NewRequest(http.MethodPost, "/api/posts/", nil)).WithUser(user)
	if err := CheckPermissions(ModelPermissions("blog", "post"))(context.Background(), modelWrite); err != nil {
		t.Fatalf("model write permission error = %v", err)
	}

	staff := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 6, IsActive: true, Authenticated: true}, IsStaff: true}}
	adminRequest := NewRequest(httptest.NewRequest(http.MethodGet, "/api/admin/", nil)).WithUser(staff)
	if err := CheckPermissions(IsAdminUser())(context.Background(), adminRequest); err != nil {
		t.Fatalf("admin permission error = %v", err)
	}

	objectPermission := CustomObjectPermission(func(_ context.Context, _ *Request, object any) bool {
		return object.(map[string]any)["owner_id"] == int64(5)
	})
	if err := CheckObjectPermissions(context.Background(), modelRead, map[string]any{"owner_id": int64(5)}, objectPermission); err != nil {
		t.Fatalf("object permission error = %v", err)
	}
	err := CheckObjectPermissions(context.Background(), modelRead, map[string]any{"owner_id": int64(99)}, objectPermission)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("object denial error = %v, want ErrPermissionDenied", err)
	}

	custom := CustomPermission(func(context.Context, *Request) bool { return true })
	if err := CheckPermissions(AllowAny(), custom)(context.Background(), anonymous); err != nil {
		t.Fatalf("custom permission error = %v", err)
	}
}

func apiFieldByName(fields []models.FieldMeta, name string) models.FieldMeta {
	for _, field := range fields {
		if field.Name == name {
			return field
		}
	}
	return models.FieldMeta{}
}
