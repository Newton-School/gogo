package admin

import (
	"errors"
	"net/http"

	"github.com/Newton-School/gogo/auth"
)

var ErrAdminPermissionDenied = errors.New("admin permission denied")

// HasAddPermission checks whether a user may add objects.
func (a ModelAdmin) HasAddPermission(r *http.Request, user auth.User) bool {
	if a.ReadOnly {
		return false
	}
	if a.Hooks.HasAddPermission != nil {
		return a.Hooks.HasAddPermission(r, user)
	}
	return user.IsActive && user.IsStaff
}

// HasChangePermission checks whether a user may change objects.
func (a ModelAdmin) HasChangePermission(r *http.Request, user auth.User) bool {
	if a.ReadOnly {
		return false
	}
	if a.Hooks.HasChangePermission != nil {
		return a.Hooks.HasChangePermission(r, user)
	}
	return user.IsActive && user.IsStaff
}

// HasDeletePermission checks whether a user may delete objects.
func (a ModelAdmin) HasDeletePermission(r *http.Request, user auth.User) bool {
	if a.ReadOnly {
		return false
	}
	if a.Hooks.HasDeletePermission != nil {
		return a.Hooks.HasDeletePermission(r, user)
	}
	return user.IsActive && user.IsStaff
}

// HasModulePermission checks whether a user may see a module.
func (a ModelAdmin) HasModulePermission(r *http.Request, user auth.User) bool {
	if a.Hooks.HasModulePermission != nil {
		return a.Hooks.HasModulePermission(r, user)
	}
	return user.IsActive && user.IsStaff
}
