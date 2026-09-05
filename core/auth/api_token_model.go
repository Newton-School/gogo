package auth

import (
	"fmt"
	"time"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

// APITokenRecord stores only a purpose-bound secret digest. AuthVersion makes
// password and grant changes invalidate issued tokens immediately on the next
// authentication. Use Tokens for issuance/revocation, never generic Admin CRUD.
type APITokenRecord struct {
	models.Base
	ID, UserID, SecretDigest string
	Scopes                   []string
	AuthVersion              int64
	ExpiresAt, CreatedAt     time.Time
	RevokedAt                *time.Time
}

func (APITokenRecord) String() string                   { return "auth.APITokenRecord{redacted}" }
func (p APITokenRecord) Format(state fmt.State, _ rune) { _, _ = fmt.Fprint(state, p.String()) }

func (*APITokenRecord) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_authtokens", Name: "APIToken", Table: "gogo_api_tokens", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.ForeignKeyField("user_id", models.Relation{Target: "gogo_auth.User", OnDelete: models.Cascade, RelatedName: "api_tokens"}, models.WithStructField("UserID"), models.ReadOnly),
		models.CharField("secret_digest", models.WithStructField("SecretDigest"), models.WithMaxLength(64), models.ReadOnly),
		models.JSONField("scopes", models.WithStructField("Scopes"), models.ReadOnly),
		models.PositiveBigIntegerField("auth_version", models.WithStructField("AuthVersion"), models.WithBounds(int64(1), nil), models.ReadOnly),
		models.DateTimeField("expires_at", models.WithStructField("ExpiresAt"), models.ReadOnly),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt"), models.ReadOnly),
		models.DateTimeField("revoked_at", models.WithStructField("RevokedAt"), models.Nullable, models.Optional, models.ReadOnly),
	}, Indexes: []models.Index{{Name: "gogo_api_tokens_expiry", Fields: []string{"expires_at"}}}}
}

// TokenSchemas/TokenMigrations are independently opt-in. A separate migration
// app avoids making API tokens depend on installing password-reset models.
func TokenSchemas() []models.Schema { return []models.Schema{(&APITokenRecord{}).Schema()} }
func TokenMigrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_authtokens", Name: "0001_initial", Dependencies: []string{"gogo_auth.0001_initial"}, Operations: []migrations.Operation{migrations.CreateModel((&APITokenRecord{}).Schema())}}}
}
