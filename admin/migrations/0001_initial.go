package migrations

import (
	coremigrations "github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/migrations/operations"
)

// Initial returns the built-in admin log initial migration.
func Initial() coremigrations.Migration {
	return coremigrations.Migration{
		AppLabel: "admin",
		Name:     coremigrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []coremigrations.Operation{
			operations.RunSQL{
				SQL: `CREATE TABLE gogo_admin_log (
	id BIGINT PRIMARY KEY,
	action_time TIMESTAMP NOT NULL,
	user_id BIGINT NOT NULL,
	content_type VARCHAR(255) NOT NULL,
	object_id VARCHAR(255) NOT NULL,
	object_repr TEXT NOT NULL,
	action_flag VARCHAR(32) NOT NULL,
	change_message TEXT NOT NULL
)`,
				ReverseSQL: `DROP TABLE gogo_admin_log`,
			},
			operations.RunSQL{
				SQL:        `CREATE INDEX gogo_admin_log_object_idx ON gogo_admin_log(content_type, object_id)`,
				ReverseSQL: `DROP INDEX gogo_admin_log_object_idx`,
			},
			operations.RunSQL{
				SQL:        `CREATE INDEX gogo_admin_log_action_time_idx ON gogo_admin_log(action_time)`,
				ReverseSQL: `DROP INDEX gogo_admin_log_action_time_idx`,
			},
		},
	}
}

// Migration is the package-level initial migration value.
var Migration = Initial()
