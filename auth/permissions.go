package auth

import (
	"sort"
	"strings"

	"github.com/Newton-School/gogo/models"
)

var defaultModelPermissions = []string{"add", "change", "delete", "view"}

// Key returns the canonical app_label.codename permission identifier.
func (p Permission) Key() string {
	app := p.AppLabel
	if p.ContentType.AppLabel != "" {
		app = p.ContentType.AppLabel
	}
	return permissionKey(app, p.Codename)
}

// GenerateModelPermissions creates default and custom permissions for models.
func GenerateModelPermissions(modelRegistry *models.Registry) ([]Permission, error) {
	if modelRegistry == nil {
		return nil, nil
	}
	contentTypes, err := NewContentTypeRegistryFromModels(modelRegistry)
	if err != nil {
		return nil, err
	}

	var permissions []Permission
	var nextID int64 = 1
	for _, meta := range modelRegistry.ContentTypeMetadata() {
		contentType, ok := contentTypes.LookupByModel(meta.AppLabel, meta.ModelName)
		if !ok {
			continue
		}
		modelName := strings.ToLower(meta.ModelName)
		verboseName := meta.VerboseName
		if verboseName == "" {
			verboseName = modelName
		}
		defaults := meta.DefaultPermissions
		if defaults == nil {
			defaults = defaultModelPermissions
		}
		for _, action := range defaults {
			permissions = append(permissions, Permission{
				ID:            nextID,
				Name:          "Can " + action + " " + verboseName,
				ContentTypeID: contentType.ID,
				Codename:      action + "_" + modelName,
				ContentType:   contentType,
				AppLabel:      contentType.AppLabel,
			})
			nextID++
		}
		for _, custom := range meta.Permissions {
			permissions = append(permissions, Permission{
				ID:            nextID,
				Name:          custom.Name,
				ContentTypeID: contentType.ID,
				Codename:      custom.CodeName,
				ContentType:   contentType,
				AppLabel:      contentType.AppLabel,
			})
			nextID++
		}
	}
	return permissions, nil
}

// HasPerm reports whether an active user has a direct, group, or superuser permission.
func HasPerm(user User, permission string) bool {
	if !user.IsActive {
		return false
	}
	if user.IsSuperuser {
		return true
	}
	return containsPermission(GetAllPermissions(user), permission)
}

// HasModulePerms reports whether an active user has any permission in an app.
func HasModulePerms(user User, appLabel string) bool {
	if !user.IsActive {
		return false
	}
	if user.IsSuperuser {
		return true
	}
	prefix := strings.ToLower(strings.TrimSpace(appLabel)) + "."
	for _, permission := range GetAllPermissions(user) {
		if strings.HasPrefix(permission, prefix) {
			return true
		}
	}
	return false
}

// GetUserPermissions returns canonical direct permission identifiers.
func GetUserPermissions(user User) []string {
	return sortedPermissionKeys(append(append([]Permission(nil), user.Permissions...), user.UserPermissions...))
}

// GetGroupPermissions returns canonical permission identifiers inherited from groups.
func GetGroupPermissions(user User) []string {
	var permissions []Permission
	for _, group := range user.Groups {
		permissions = append(permissions, group.Permissions...)
	}
	return sortedPermissionKeys(permissions)
}

// GetAllPermissions returns canonical direct and group permission identifiers.
func GetAllPermissions(user User) []string {
	return sortedStrings(uniqueStrings(append(GetUserPermissions(user), GetGroupPermissions(user)...)))
}

// WithPerm filters active users that have a permission.
func WithPerm(users []User, permission string) []User {
	filtered := make([]User, 0, len(users))
	for _, user := range users {
		if HasPerm(user, permission) {
			filtered = append(filtered, user)
		}
	}
	return filtered
}

func sortedPermissionKeys(permissions []Permission) []string {
	keys := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		key := permission.Key()
		if key != "." && !strings.HasSuffix(key, ".") {
			keys = append(keys, key)
		}
	}
	return sortedStrings(uniqueStrings(keys))
}

func containsPermission(permissions []string, permission string) bool {
	needle := strings.ToLower(strings.TrimSpace(permission))
	for _, candidate := range permissions {
		if candidate == needle {
			return true
		}
	}
	return false
}

func permissionKey(appLabel, codename string) string {
	return strings.ToLower(strings.TrimSpace(appLabel)) + "." + strings.ToLower(strings.TrimSpace(codename))
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func sortedStrings(values []string) []string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return copied
}
