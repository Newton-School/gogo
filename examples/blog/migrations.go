package blog

import (
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/migrations/operations"
)

func Migrations() []migrations.Migration {
	return []migrations.Migration{
		{
			AppLabel: blogAppLabel,
			Name:     migrations.InitialMigrationName(),
			Dependencies: []migrations.Dependency{
				{AppLabel: "auth", Name: migrations.InitialMigrationName()},
			},
			Atomic: true,
			Operations: []migrations.Operation{
				operations.RunSQL{
					SQL: `CREATE TABLE blog_author (
	id BIGSERIAL PRIMARY KEY,
	user_id BIGINT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
	display_name VARCHAR(150) NOT NULL,
	bio TEXT NOT NULL DEFAULT '',
	website VARCHAR(300) NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE blog_tag (
	id BIGSERIAL PRIMARY KEY,
	name VARCHAR(80) NOT NULL,
	slug VARCHAR(90) NOT NULL UNIQUE
);
CREATE TABLE blog_post (
	id BIGSERIAL PRIMARY KEY,
	author_id BIGINT NOT NULL REFERENCES blog_author(id) ON DELETE RESTRICT,
	title VARCHAR(220) NOT NULL,
	slug VARCHAR(240) NOT NULL UNIQUE,
	body TEXT NOT NULL,
	status VARCHAR(20) NOT NULL,
	published_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE blog_post_tags (
	id BIGSERIAL PRIMARY KEY,
	post_id BIGINT NOT NULL REFERENCES blog_post(id) ON DELETE CASCADE,
	tag_id BIGINT NOT NULL REFERENCES blog_tag(id) ON DELETE CASCADE,
	UNIQUE (post_id, tag_id)
);
CREATE TABLE blog_comment (
	id BIGSERIAL PRIMARY KEY,
	post_id BIGINT NOT NULL REFERENCES blog_post(id) ON DELETE CASCADE,
	name VARCHAR(120) NOT NULL,
	email VARCHAR(254) NOT NULL,
	body TEXT NOT NULL,
	status VARCHAR(20) NOT NULL,
	ip_address VARCHAR(64) NOT NULL DEFAULT '',
	user_agent VARCHAR(255) NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE blog_audit_event (
	id BIGSERIAL PRIMARY KEY,
	actor_id BIGINT REFERENCES auth_user(id) ON DELETE SET NULL,
	object_type VARCHAR(80) NOT NULL,
	object_id VARCHAR(120) NOT NULL,
	action VARCHAR(80) NOT NULL,
	payload JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX blog_post_status_pub_idx ON blog_post(status, published_at DESC);
CREATE INDEX blog_post_author_idx ON blog_post(author_id);
CREATE INDEX blog_comment_post_status_idx ON blog_comment(post_id, status);
CREATE INDEX blog_audit_object_idx ON blog_audit_event(object_type, object_id);`,
					ReverseSQL: `DROP TABLE blog_audit_event;
DROP TABLE blog_comment;
DROP TABLE blog_post_tags;
DROP TABLE blog_post;
DROP TABLE blog_tag;
DROP TABLE blog_author;`,
				},
			},
		},
	}
}
