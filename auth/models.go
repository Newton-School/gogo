package auth

import (
	"time"

	"github.com/cybersaksham/gogo/models"
)

const (
	appLabel              = "auth"
	permissionModel       = "Permission"
	groupModel            = "Group"
	userModel             = "User"
	permissionTable       = "auth_permission"
	groupTable            = "auth_group"
	userTable             = "auth_user"
	permissionTarget      = "auth.Permission"
	groupTarget           = "auth.Group"
	contentTypeTarget     = "auth.ContentType"
	defaultManagerName    = "objects"
	defaultBaseManager    = "objects"
	defaultPermissionName = "permission"
	defaultGroupName      = "group"
	defaultUserName       = "user"
)

// Permission grants one model-level capability for a content type.
type Permission struct {
	ID            int64
	Name          string
	ContentTypeID int64
	Codename      string
	ContentType   ContentType
	AppLabel      string
}

// ModelMeta returns Django-compatible metadata for auth permissions.
func (Permission) ModelMeta() models.Metadata {
	return authMetadata(models.Metadata{
		AppLabel:          appLabel,
		ModelName:         permissionModel,
		TableName:         permissionTable,
		DBTable:           permissionTable,
		VerboseName:       defaultPermissionName,
		VerboseNamePlural: "permissions",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "name", Column: "name", VerboseName: "Name"},
			{Name: "content_type", Column: "content_type_id", RelationTarget: contentTypeTarget, RelationType: "foreign_key", DeleteBehavior: "cascade", VerboseName: "Content type"},
			{Name: "codename", Column: "codename", VerboseName: "Codename"},
		},
		Constraints: []models.Constraint{
			{Name: "auth_permission_content_type_codename_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("content_type_id"), models.Asc("codename")}},
		},
	})
}

// Group stores a named collection of permissions.
type Group struct {
	ID          int64
	Name        string
	Permissions []Permission
}

// ModelMeta returns Django-compatible metadata for auth groups.
func (Group) ModelMeta() models.Metadata {
	return authMetadata(models.Metadata{
		AppLabel:          appLabel,
		ModelName:         groupModel,
		TableName:         groupTable,
		DBTable:           groupTable,
		VerboseName:       defaultGroupName,
		VerboseNamePlural: "groups",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "name", Column: "name", VerboseName: "Name"},
			{Name: "permissions", RelationTarget: permissionTarget, RelationType: "many_to_many", Blank: true, VerboseName: "Permissions"},
		},
		Constraints: []models.Constraint{
			{Name: "auth_group_name_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("name")}},
		},
	})
}

// AbstractBaseUser contains the core identity fields shared by user types.
type AbstractBaseUser struct {
	ID              int64
	Password        string
	LastLogin       time.Time
	IsSuperuser     bool
	IsActive        bool
	DateJoined      time.Time
	Groups          []Group
	Permissions     []Permission
	UserPermissions []Permission
	Anonymous       bool
	Authenticated   bool
}

// IsAuthenticated reports whether this principal represents a logged-in user.
func (u AbstractBaseUser) IsAuthenticated() bool {
	return u.Authenticated || (!u.Anonymous && u.ID != 0)
}

// IsAnonymous reports whether this principal represents an anonymous request.
func (u AbstractBaseUser) IsAnonymous() bool {
	return u.Anonymous
}

// AbstractUser adds the default Django username, contact, and staff fields.
type AbstractUser struct {
	AbstractBaseUser
	Username  string
	FirstName string
	LastName  string
	Email     string
	IsStaff   bool
}

// User is the framework-owned built-in user model.
type User struct {
	AbstractUser
}

// ModelMeta returns Django-compatible metadata for auth users.
func (User) ModelMeta() models.Metadata {
	return userMetadata(false)
}

// AbstractBaseUserMetadata exposes the embeddable base-user metadata.
func AbstractBaseUserMetadata() models.Metadata {
	meta := userMetadata(true)
	meta.ModelName = "AbstractBaseUser"
	meta.TableName = ""
	meta.DBTable = ""
	meta.VerboseName = "abstract base user"
	meta.VerboseNamePlural = "abstract base users"
	meta.Fields = meta.Fields[:4]
	return meta
}

// AbstractUserMetadata exposes the embeddable default-user metadata.
func AbstractUserMetadata() models.Metadata {
	meta := userMetadata(true)
	meta.ModelName = "AbstractUser"
	meta.TableName = ""
	meta.DBTable = ""
	meta.VerboseName = "abstract user"
	meta.VerboseNamePlural = "abstract users"
	return meta
}

// ModelMetadata returns the framework-owned auth models that client projects
// should include by default.
func ModelMetadata() []models.Metadata {
	return []models.Metadata{
		ContentType{}.ModelMeta(),
		Permission{}.ModelMeta(),
		Group{}.ModelMeta(),
		User{}.ModelMeta(),
	}
}

func userMetadata(abstract bool) models.Metadata {
	return authMetadata(models.Metadata{
		AppLabel:          appLabel,
		ModelName:         userModel,
		TableName:         userTable,
		DBTable:           userTable,
		VerboseName:       defaultUserName,
		VerboseNamePlural: "users",
		Abstract:          abstract,
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "password", Column: "password", Kind: "password_hash", VerboseName: "Password", HelpText: "Raw passwords are not stored, so there is no way to see the user's password.", Blank: true},
			{Name: "last_login", Column: "last_login", Kind: "datetime", Null: true, Blank: true, VerboseName: "Last login"},
			{Name: "is_superuser", Column: "is_superuser", Kind: "boolean", Default: false, VerboseName: "Superuser status", HelpText: "Designates that this user has all permissions without explicitly assigning them."},
			{Name: "username", Column: "username", Kind: "char", MaxLength: 150, VerboseName: "Username", HelpText: "Required. 150 characters or fewer. Letters, digits and @/./+/-/_ only."},
			{Name: "first_name", Column: "first_name", Kind: "char", MaxLength: 150, Blank: true, Default: "", VerboseName: "First name"},
			{Name: "last_name", Column: "last_name", Kind: "char", MaxLength: 150, Blank: true, Default: "", VerboseName: "Last name"},
			{Name: "email", Column: "email", Kind: "email", MaxLength: 254, Blank: true, Default: "", VerboseName: "Email address"},
			{Name: "is_staff", Column: "is_staff", Kind: "boolean", Default: false, VerboseName: "Staff status", HelpText: "Designates whether the user can log into this admin site."},
			{Name: "is_active", Column: "is_active", Kind: "boolean", Default: true, VerboseName: "Active", HelpText: "Designates whether this user should be treated as active. Unselect this instead of deleting accounts."},
			{Name: "date_joined", Column: "date_joined", Kind: "datetime", VerboseName: "Date joined"},
			{Name: "groups", RelationTarget: groupTarget, RelationType: "many_to_many", Blank: true, VerboseName: "Groups", HelpText: "The groups this user belongs to. A user will get all permissions granted to each of their groups. Hold down \"Control\", or \"Command\" on a Mac, to select more than one."},
			{Name: "user_permissions", RelationTarget: permissionTarget, RelationType: "many_to_many", Blank: true, VerboseName: "User permissions", HelpText: "Specific permissions for this user. Hold down \"Control\", or \"Command\" on a Mac, to select more than one."},
		},
		Constraints: []models.Constraint{
			{Name: "auth_user_username_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("username")}},
		},
	})
}

func authMetadata(meta models.Metadata) models.Metadata {
	meta.DefaultManagerName = defaultManagerName
	meta.BaseManagerName = defaultBaseManager
	meta.DefaultPermissions = []string{"add", "change", "delete", "view"}
	return meta
}
