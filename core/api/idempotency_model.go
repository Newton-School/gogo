package api

import (
	"errors"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

// IdempotencyRecord is private operation data, not a model to expose through
// generic resources or Admin. Requests and plaintext keys are never stored.
// Install its migration explicitly before enabling the service.
type IdempotencyRecord struct {
	models.Base
	ID, ScopeHash, Action, KeyDigest, RequestDigest string
	ResponseStatus                                  int
	ResponseHeaders                                 map[string]string
	ResponseBody, ObjectKey                         map[string]any
	CreatedAt, ExpiresAt                            time.Time
}

func (IdempotencyRecord) String() string               { return "api.IdempotencyRecord{redacted}" }
func (r IdempotencyRecord) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, r.String()) }
func (IdempotencyRecord) MarshalJSON() ([]byte, error) {
	return nil, errors.New("api: private operation receipt cannot be serialized directly")
}

func (*IdempotencyRecord) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_api", Name: "Idempotency", Table: "gogo_idempotency", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.CharField("scope_hash", models.WithStructField("ScopeHash"), models.WithMaxLength(64), models.ReadOnly),
		models.CharField("action", models.WithStructField("Action"), models.WithMaxLength(128), models.ReadOnly),
		models.CharField("key_digest", models.WithStructField("KeyDigest"), models.WithMaxLength(64), models.ReadOnly),
		models.CharField("request_digest", models.WithStructField("RequestDigest"), models.WithMaxLength(64), models.ReadOnly),
		models.IntegerField("response_status", models.WithStructField("ResponseStatus"), models.ReadOnly),
		models.JSONField("response_headers", models.WithStructField("ResponseHeaders"), models.ReadOnly),
		models.JSONField("response_body", models.WithStructField("ResponseBody"), models.ReadOnly),
		models.JSONField("object_key", models.WithStructField("ObjectKey"), models.ReadOnly),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt"), models.ReadOnly),
		models.DateTimeField("expires_at", models.WithStructField("ExpiresAt"), models.ReadOnly),
	}, Constraints: []models.Constraint{{Name: "gogo_idempotency_identity", Kind: "unique", Fields: []string{"scope_hash", "action", "key_digest"}}}, Indexes: []models.Index{{Name: "gogo_idempotency_expiry", Fields: []string{"expires_at"}}}}
}

func IdempotencySchemas() []models.Schema { return []models.Schema{(&IdempotencyRecord{}).Schema()} }
func IdempotencyMigrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_api", Name: "0001_idempotency", Operations: []migrations.Operation{migrations.CreateModel((&IdempotencyRecord{}).Schema())}}}
}
