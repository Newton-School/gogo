package auth

import (
	"fmt"
	"time"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

// PasswordResetRecord contains a digest, never the reset bearer secret. Its
// account version makes every earlier token invalid after a credential/grant
// transition. Removal is explicit maintenance, never import-time cleanup.
type PasswordResetRecord struct {
	models.Base
	ID, UserID, SecretDigest string
	AuthVersion              int64
	ExpiresAt, CreatedAt     time.Time
	UsedAt                   *time.Time
}

func (PasswordResetRecord) String() string                   { return "auth.PasswordResetRecord{redacted}" }
func (p PasswordResetRecord) GoString() string               { return p.String() }
func (p PasswordResetRecord) Format(state fmt.State, _ rune) { _, _ = fmt.Fprint(state, p.String()) }

func (*PasswordResetRecord) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_auth", Name: "PasswordReset", Table: "gogo_password_resets", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.ForeignKeyField("user_id", models.Relation{Target: "gogo_auth.User", OnDelete: models.Cascade, RelatedName: "password_resets"}, models.WithStructField("UserID"), models.ReadOnly),
		models.CharField("secret_digest", models.WithStructField("SecretDigest"), models.WithMaxLength(64), models.ReadOnly),
		models.PositiveBigIntegerField("auth_version", models.WithStructField("AuthVersion"), models.WithBounds(int64(1), nil), models.ReadOnly),
		models.DateTimeField("expires_at", models.WithStructField("ExpiresAt"), models.ReadOnly),
		models.DateTimeField("used_at", models.WithStructField("UsedAt"), models.Nullable, models.Optional, models.ReadOnly),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt"), models.ReadOnly),
	}, Indexes: []models.Index{{Name: "gogo_password_resets_expiry", Fields: []string{"expires_at"}}}}
}

// PasswordResetSchemas are opt-in additions to Schemas. Keep the initial
// account migration immutable when enabling resets on an existing project.
func PasswordResetSchemas() []models.Schema {
	return []models.Schema{(&PasswordResetRecord{}).Schema()}
}
func PasswordResetMigrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_auth", Name: "0002_password_resets", Dependencies: []string{"gogo_auth.0001_initial"}, Operations: []migrations.Operation{migrations.CreateModel((&PasswordResetRecord{}).Schema())}}}
}
