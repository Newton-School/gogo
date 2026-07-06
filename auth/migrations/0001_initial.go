package migrations

import (
	coremigrations "github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/migrations/operations"
)

// Initial returns the built-in auth and session initial migration.
func Initial() coremigrations.Migration {
	return coremigrations.Migration{
		AppLabel: "auth",
		Name:     coremigrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []coremigrations.Operation{
			operations.RunSQL{
				SQL: `CREATE TABLE gogo_content_type (
	id BIGINT PRIMARY KEY,
	app_label VARCHAR(100) NOT NULL,
	model VARCHAR(100) NOT NULL,
	UNIQUE(app_label, model)
)`,
				ReverseSQL: `DROP TABLE gogo_content_type`,
			},
			operations.RunSQL{
				SQL: `CREATE TABLE auth_permission (
	id BIGINT PRIMARY KEY,
	name VARCHAR(255) NOT NULL,
	content_type_id BIGINT NOT NULL,
	codename VARCHAR(100) NOT NULL,
	FOREIGN KEY(content_type_id) REFERENCES gogo_content_type(id) ON DELETE CASCADE,
	UNIQUE(content_type_id, codename)
)`,
				ReverseSQL: `DROP TABLE auth_permission`,
			},
			operations.RunSQL{
				SQL:        `CREATE INDEX auth_permission_content_type_idx ON auth_permission(content_type_id)`,
				ReverseSQL: `DROP INDEX auth_permission_content_type_idx`,
			},
			operations.RunSQL{
				SQL: `CREATE TABLE auth_group (
	id BIGINT PRIMARY KEY,
	name VARCHAR(150) NOT NULL UNIQUE
)`,
				ReverseSQL: `DROP TABLE auth_group`,
			},
			operations.RunSQL{
				SQL: `CREATE TABLE auth_group_permissions (
	id BIGINT PRIMARY KEY,
	group_id BIGINT NOT NULL,
	permission_id BIGINT NOT NULL,
	FOREIGN KEY(group_id) REFERENCES auth_group(id) ON DELETE CASCADE,
	FOREIGN KEY(permission_id) REFERENCES auth_permission(id) ON DELETE CASCADE,
	UNIQUE(group_id, permission_id)
)`,
				ReverseSQL: `DROP TABLE auth_group_permissions`,
			},
			operations.RunSQL{
				SQL: `CREATE TABLE auth_user (
	id BIGINT PRIMARY KEY,
	password VARCHAR(128) NOT NULL,
	last_login TIMESTAMP NULL,
	is_superuser BOOLEAN NOT NULL DEFAULT FALSE,
	username VARCHAR(150) NOT NULL UNIQUE,
	first_name VARCHAR(150) NOT NULL DEFAULT '',
	last_name VARCHAR(150) NOT NULL DEFAULT '',
	email VARCHAR(254) NOT NULL DEFAULT '',
	is_staff BOOLEAN NOT NULL DEFAULT FALSE,
	is_active BOOLEAN NOT NULL DEFAULT TRUE,
	date_joined TIMESTAMP NOT NULL
)`,
				ReverseSQL: `DROP TABLE auth_user`,
			},
			operations.RunSQL{
				SQL:        `CREATE INDEX auth_user_email_idx ON auth_user(email)`,
				ReverseSQL: `DROP INDEX auth_user_email_idx`,
			},
			operations.RunSQL{
				SQL: `CREATE TABLE auth_user_groups (
	id BIGINT PRIMARY KEY,
	user_id BIGINT NOT NULL,
	group_id BIGINT NOT NULL,
	FOREIGN KEY(user_id) REFERENCES auth_user(id) ON DELETE CASCADE,
	FOREIGN KEY(group_id) REFERENCES auth_group(id) ON DELETE CASCADE,
	UNIQUE(user_id, group_id)
)`,
				ReverseSQL: `DROP TABLE auth_user_groups`,
			},
			operations.RunSQL{
				SQL: `CREATE TABLE auth_user_user_permissions (
	id BIGINT PRIMARY KEY,
	user_id BIGINT NOT NULL,
	permission_id BIGINT NOT NULL,
	FOREIGN KEY(user_id) REFERENCES auth_user(id) ON DELETE CASCADE,
	FOREIGN KEY(permission_id) REFERENCES auth_permission(id) ON DELETE CASCADE,
	UNIQUE(user_id, permission_id)
)`,
				ReverseSQL: `DROP TABLE auth_user_user_permissions`,
			},
			operations.RunSQL{
				SQL: `CREATE TABLE gogo_session (
	session_key VARCHAR(255) PRIMARY KEY,
	session_data TEXT NOT NULL,
	expire_date TIMESTAMP NOT NULL
)`,
				ReverseSQL: `DROP TABLE gogo_session`,
			},
			operations.RunSQL{
				SQL:        `CREATE INDEX gogo_session_expire_date_idx ON gogo_session(expire_date)`,
				ReverseSQL: `DROP INDEX gogo_session_expire_date_idx`,
			},
		},
	}
}

// Migration is the package-level initial migration value.
var Migration = Initial()
