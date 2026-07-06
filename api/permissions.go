package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Newton-School/gogo/auth"
)

// PermissionClass evaluates request and object-level API access.
type PermissionClass interface {
	HasPermission(context.Context, *Request) bool
	HasObjectPermission(context.Context, *Request, any) bool
}

type permissionClass struct {
	hasPermission       func(context.Context, *Request) bool
	hasObjectPermission func(context.Context, *Request, any) bool
}

func (p permissionClass) HasPermission(ctx context.Context, request *Request) bool {
	return p.hasPermission == nil || p.hasPermission(ctx, request)
}

func (p permissionClass) HasObjectPermission(ctx context.Context, request *Request, object any) bool {
	if p.hasObjectPermission != nil {
		return p.hasObjectPermission(ctx, request, object)
	}
	return p.HasPermission(ctx, request)
}

// AllowAny allows every request.
func AllowAny() PermissionClass {
	return permissionClass{hasPermission: func(context.Context, *Request) bool { return true }}
}

// IsAuthenticated allows active authenticated users.
func IsAuthenticated() PermissionClass {
	return permissionClass{hasPermission: func(_ context.Context, request *Request) bool {
		return isAuthenticatedUser(apiUser(request))
	}}
}

// IsAdminUser allows active staff users.
func IsAdminUser() PermissionClass {
	return permissionClass{hasPermission: func(_ context.Context, request *Request) bool {
		user := apiUser(request)
		return isAuthenticatedUser(user) && user.IsStaff
	}}
}

// IsAuthenticatedOrReadOnly allows safe methods or authenticated writes.
func IsAuthenticatedOrReadOnly() PermissionClass {
	return permissionClass{hasPermission: func(_ context.Context, request *Request) bool {
		return isSafeAPIMethod(request.Method()) || isAuthenticatedUser(apiUser(request))
	}}
}

// ModelPermissions maps HTTP methods to Django-style model permissions.
func ModelPermissions(appLabel, model string) PermissionClass {
	return permissionClass{hasPermission: func(_ context.Context, request *Request) bool {
		action, ok := modelPermissionAction(request.Method())
		if !ok {
			return false
		}
		user := apiUser(request)
		if !isAuthenticatedUser(user) {
			return false
		}
		permission := fmt.Sprintf("%s.%s_%s", strings.ToLower(strings.TrimSpace(appLabel)), action, strings.ToLower(strings.TrimSpace(model)))
		return auth.HasPerm(user, permission)
	}}
}

// CustomPermission creates a request-level permission class.
func CustomPermission(check func(context.Context, *Request) bool) PermissionClass {
	return permissionClass{hasPermission: check}
}

// CustomObjectPermission creates an object-level permission class.
func CustomObjectPermission(check func(context.Context, *Request, any) bool) PermissionClass {
	return permissionClass{
		hasPermission:       func(context.Context, *Request) bool { return true },
		hasObjectPermission: check,
	}
}

// CheckPermissions creates an APIView permission lifecycle hook.
func CheckPermissions(classes ...PermissionClass) RequestHook {
	return func(ctx context.Context, request *Request) error {
		for _, class := range classes {
			if class == nil {
				continue
			}
			if !class.HasPermission(ctx, request) {
				return ErrPermissionDenied
			}
		}
		return nil
	}
}

// CheckObjectPermissions evaluates object-level permissions.
func CheckObjectPermissions(ctx context.Context, request *Request, object any, classes ...PermissionClass) error {
	for _, class := range classes {
		if class == nil {
			continue
		}
		if !class.HasObjectPermission(ctx, request, object) {
			return ErrPermissionDenied
		}
	}
	return nil
}

func apiUser(request *Request) auth.User {
	user := request.User()
	if user.ID == 0 && !user.Authenticated {
		return auth.AnonymousUser()
	}
	return user
}

func isAuthenticatedUser(user auth.User) bool {
	return user.IsActive && user.IsAuthenticated() && !user.IsAnonymous()
}

func isSafeAPIMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func modelPermissionAction(method string) (string, bool) {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "view", true
	case http.MethodPost:
		return "add", true
	case http.MethodPut, http.MethodPatch:
		return "change", true
	case http.MethodDelete:
		return "delete", true
	default:
		return "", false
	}
}
