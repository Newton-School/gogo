package sessions

import (
	"encoding/json"
	"time"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

// DatabaseSession declares the session provider's private persistence record.
// Applications should mutate sessions through Store, not ordinary model CRUD:
// Store owns compare-and-set, expiry and logout tombstone semantics. The digest
// is not a usable browser credential; the random bearer key is never persisted.
type DatabaseSession struct {
	models.Base
	KeyDigest                       string
	Payload                         json.RawMessage
	Version                         int64
	ExpiresAt, CreatedAt, UpdatedAt time.Time
	DeletedAt, TombstoneUntil       *time.Time
}

func (*DatabaseSession) String() string   { return "sessions.DatabaseSession{redacted}" }
func (*DatabaseSession) GoString() string { return "sessions.DatabaseSession{redacted}" }

func (*DatabaseSession) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_sessions", Name: "Session", Table: "gogo_sessions", Fields: []models.Field{
		models.CharField("key_digest", models.WithStructField("KeyDigest"), models.WithMaxLength(64), models.Primary, models.ReadOnly),
		models.JSONField("payload", models.WithStructField("Payload"), models.ReadOnly),
		models.PositiveBigIntegerField("version", models.WithStructField("Version"), models.WithBounds(int64(1), nil), models.ReadOnly),
		models.DateTimeField("expires_at", models.WithStructField("ExpiresAt"), models.ReadOnly),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt"), models.ReadOnly),
		models.DateTimeField("updated_at", models.WithStructField("UpdatedAt"), models.ReadOnly),
		models.DateTimeField("deleted_at", models.WithStructField("DeletedAt"), models.Nullable, models.Optional, models.ReadOnly),
		models.DateTimeField("tombstone_until", models.WithStructField("TombstoneUntil"), models.Nullable, models.Optional, models.ReadOnly),
	}, Indexes: []models.Index{{Name: "gogo_sessions_expiry", Fields: []string{"expires_at"}}, {Name: "gogo_sessions_tombstones", Fields: []string{"tombstone_until"}}}}
}

// Migrations must be registered with the project's explicit migrate command.
// Selecting a session provider never creates tables during startup or requests.
func Migrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_sessions", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel((&DatabaseSession{}).Schema())}}}
}
