package auth

import (
	"time"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

// User is the default persisted identity. Applications with a custom user model
// implement PasswordBackend and PrincipalLoader instead. Select that model
// before initial migrations; changing an existing identity table is not an
// automatic runtime swap. Account mutations should use Accounts, which owns
// normalized identities, grant authorization and auth-version invalidation.
type User struct {
	models.Base
	identity                 AccountModels
	ID, Identifier           string
	PasswordHash             *string `json:"-"`
	Active, Staff, Superuser bool
	AuthVersion              int64
	LastLogin                *time.Time
	CreatedAt, UpdatedAt     time.Time
}

// Formatting whole account records must never disclose a password hash.
func (*User) String() string   { return "auth.User{redacted}" }
func (*User) GoString() string { return "auth.User{redacted}" }

func (u *User) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_auth", Name: "User", Table: "gogo_users", Fields: []models.Field{
		u.identity.idField(),
		models.CharField("identifier", models.WithStructField("Identifier"), models.WithMaxLength(255)),
		models.TextField("password_hash", models.WithStructField("PasswordHash"), models.Nullable, models.Optional, models.ReadOnly),
		models.BooleanField("active", models.WithStructField("Active"), models.WithDefault(true)),
		models.BooleanField("staff", models.WithStructField("Staff"), models.WithDefault(false)),
		models.BooleanField("superuser", models.WithStructField("Superuser"), models.WithDefault(false)),
		models.PositiveBigIntegerField("auth_version", models.WithStructField("AuthVersion"), models.WithDefault(int64(1)), models.WithBounds(int64(1), nil), models.ReadOnly),
		models.DateTimeField("last_login", models.WithStructField("LastLogin"), models.Nullable, models.Optional, models.ReadOnly),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt"), func(f *models.Field) { f.AutoNowAdd = true }),
		models.DateTimeField("updated_at", models.WithStructField("UpdatedAt"), func(f *models.Field) { f.AutoNow = true }),
	}, Constraints: []models.Constraint{{Name: "gogo_users_identifier", Kind: "unique", Fields: []string{"identifier"}}}, Ordering: []string{"identifier"}}
}

type Group struct {
	models.Base
	identity AccountModels
	ID, Name string
}

func (g *Group) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_auth", Name: "Group", Table: "gogo_groups", Fields: []models.Field{
		g.identity.idField(),
		models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(150)),
	}, Constraints: []models.Constraint{{Name: "gogo_groups_name", Kind: "unique", Fields: []string{"name"}}}, Ordering: []string{"name"}}
}

type Permission struct {
	models.Base
	ID, ContentTypeID int64
	Codename, Name    string
}

func (*Permission) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_auth", Name: "Permission", Table: "gogo_permissions", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.ForeignKeyField("content_type_id", models.Relation{Target: "gogo_contenttypes.ContentType", OnDelete: models.Protect, RelatedName: "permissions"}, models.WithStructField("ContentTypeID")),
		models.CharField("codename", models.WithStructField("Codename"), models.WithMaxLength(128)),
		models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(255)),
	}, Constraints: []models.Constraint{{Name: "gogo_permissions_identity", Kind: "unique", Fields: []string{"content_type_id", "codename"}}}, Ordering: []string{"content_type_id", "codename"}}
}

type UserGroup struct {
	models.Base
	ID              int64
	UserID, GroupID string
}

func (*UserGroup) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_auth", Name: "UserGroup", Table: "gogo_user_groups", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.ForeignKeyField("user_id", models.Relation{Target: "gogo_auth.User", OnDelete: models.Cascade, RelatedName: "group_memberships"}, models.WithStructField("UserID")),
		models.ForeignKeyField("group_id", models.Relation{Target: "gogo_auth.Group", OnDelete: models.Cascade, RelatedName: "user_memberships"}, models.WithStructField("GroupID")),
	}, Constraints: []models.Constraint{{Name: "gogo_user_groups_pair", Kind: "unique", Fields: []string{"user_id", "group_id"}}}}
}

type UserPermission struct {
	models.Base
	ID, PermissionID int64
	UserID           string
}

func (*UserPermission) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_auth", Name: "UserPermission", Table: "gogo_user_permissions", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.ForeignKeyField("user_id", models.Relation{Target: "gogo_auth.User", OnDelete: models.Cascade, RelatedName: "direct_permissions"}, models.WithStructField("UserID")),
		models.ForeignKeyField("permission_id", models.Relation{Target: "gogo_auth.Permission", OnDelete: models.Cascade, RelatedName: "user_grants"}, models.WithStructField("PermissionID")),
	}, Constraints: []models.Constraint{{Name: "gogo_user_permissions_pair", Kind: "unique", Fields: []string{"user_id", "permission_id"}}}}
}

type GroupPermission struct {
	models.Base
	ID, PermissionID int64
	GroupID          string
}

func (*GroupPermission) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_auth", Name: "GroupPermission", Table: "gogo_group_permissions", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.ForeignKeyField("group_id", models.Relation{Target: "gogo_auth.Group", OnDelete: models.Cascade, RelatedName: "permission_grants"}, models.WithStructField("GroupID")),
		models.ForeignKeyField("permission_id", models.Relation{Target: "gogo_auth.Permission", OnDelete: models.Cascade, RelatedName: "group_grants"}, models.WithStructField("PermissionID")),
	}, Constraints: []models.Constraint{{Name: "gogo_group_permissions_pair", Kind: "unique", Fields: []string{"group_id", "permission_id"}}}}
}

func Schemas() []models.Schema {
	return (AccountModels{}).Schemas()
}

// Schemas returns account schemas with the selected User/Group ID type.
func (m AccountModels) Schemas() []models.Schema {
	return []models.Schema{m.User().Schema(), m.Group().Schema(), (&Permission{}).Schema(), (&UserGroup{}).Schema(), (&UserPermission{}).Schema(), (&GroupPermission{}).Schema()}
}

// Migrations requires the separately listed contenttypes.Migrations first.
// Neither import nor Accounts construction runs these automatically.
func Migrations() []migrations.Migration {
	return (AccountModels{}).Migrations()
}

// Migrations is the initial schema for this ID choice, not an ID conversion.
// Existing UUID databases must keep NewAccountModels(models.UUID).Migrations().
func (m AccountModels) Migrations() []migrations.Migration {
	var operations []migrations.Operation
	for _, schema := range m.Schemas() {
		operations = append(operations, migrations.CreateModel(schema))
	}
	return []migrations.Migration{{App: "gogo_auth", Name: "0001_initial", Dependencies: []string{"gogo_contenttypes.0001_initial"}, Operations: operations}}
}
