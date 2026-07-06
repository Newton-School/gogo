package blog

import (
	"time"

	"github.com/Newton-School/gogo/models"
)

type ModelMeta = models.Metadata
type FieldMeta = models.FieldMeta

const blogAppLabel = "blog"

// Author stores public author profile data linked to the built-in auth user.
type Author struct {
	models.BaseModel
	UserID      int64
	DisplayName string
	Bio         string
	Website     string
}

func (Author) ModelMeta() models.Metadata {
	return modelMeta("Author", "blog_author", "author", "authors", []models.FieldMeta{
		{Name: "id", Column: "id", PrimaryKey: true},
		{Name: "user", Column: "user_id", RelationTarget: "auth.User", DeleteBehavior: "cascade"},
		{Name: "display_name", Column: "display_name"},
		{Name: "bio", Column: "bio"},
		{Name: "website", Column: "website"},
		{Name: "created_at", Column: "created_at"},
		{Name: "updated_at", Column: "updated_at"},
	})
}

// Tag categorizes posts and is exposed through the API and admin.
type Tag struct {
	models.BaseModel
	Name string
	Slug string
}

func (Tag) ModelMeta() models.Metadata {
	meta := modelMeta("Tag", "blog_tag", "tag", "tags", []models.FieldMeta{
		{Name: "id", Column: "id", PrimaryKey: true},
		{Name: "name", Column: "name"},
		{Name: "slug", Column: "slug"},
	})
	meta.Constraints = []models.Constraint{
		{Name: "blog_tag_slug_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("slug")}},
	}
	return meta
}

// Post is the central content model for the example application.
type Post struct {
	models.BaseModel
	AuthorID    int64
	Title       string
	Slug        string
	Body        string
	Status      string
	PublishedAt *time.Time
	Tags        []Tag
}

func (Post) ModelMeta() models.Metadata {
	meta := modelMeta("Post", "blog_post", "post", "posts", []models.FieldMeta{
		{Name: "id", Column: "id", PrimaryKey: true},
		{Name: "author", Column: "author_id", RelationTarget: "blog.Author", DeleteBehavior: "protect"},
		{Name: "title", Column: "title"},
		{Name: "slug", Column: "slug"},
		{Name: "body", Column: "body"},
		{Name: "status", Column: "status"},
		{Name: "published_at", Column: "published_at"},
		{Name: "tags", RelationTarget: "blog.Tag"},
		{Name: "created_at", Column: "created_at"},
		{Name: "updated_at", Column: "updated_at"},
	})
	meta.Ordering = []string{"-published_at", "-created_at"}
	meta.GetLatestBy = []string{"published_at"}
	meta.Indexes = []models.Index{
		{Name: "blog_post_status_pub_idx", Fields: []models.IndexField{models.Asc("status"), models.Desc("published_at")}},
		{Name: "blog_post_author_idx", Fields: []models.IndexField{models.Asc("author_id")}},
	}
	meta.Constraints = []models.Constraint{
		{Name: "blog_post_slug_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("slug")}},
	}
	meta.Permissions = []models.Permission{
		{CodeName: "publish_post", Name: "Can publish post"},
		{CodeName: "feature_post", Name: "Can feature post"},
	}
	return meta
}

// Comment stores moderated reader feedback for posts.
type Comment struct {
	models.BaseModel
	PostID    int64
	Name      string
	Email     string
	Body      string
	Status    string
	IPAddress string
	UserAgent string
}

func (Comment) ModelMeta() models.Metadata {
	meta := modelMeta("Comment", "blog_comment", "comment", "comments", []models.FieldMeta{
		{Name: "id", Column: "id", PrimaryKey: true},
		{Name: "post", Column: "post_id", RelationTarget: "blog.Post", DeleteBehavior: "cascade"},
		{Name: "name", Column: "name"},
		{Name: "email", Column: "email"},
		{Name: "body", Column: "body"},
		{Name: "status", Column: "status"},
		{Name: "ip_address", Column: "ip_address"},
		{Name: "user_agent", Column: "user_agent"},
		{Name: "created_at", Column: "created_at"},
		{Name: "updated_at", Column: "updated_at"},
	})
	meta.Ordering = []string{"-created_at"}
	meta.Indexes = []models.Index{
		{Name: "blog_comment_post_status_idx", Fields: []models.IndexField{models.Asc("post_id"), models.Asc("status")}},
	}
	return meta
}

// AuditEvent records content-management actions for production traceability.
type AuditEvent struct {
	ID         int64
	ActorID    int64
	ObjectType string
	ObjectID   string
	Action     string
	Payload    map[string]any
	CreatedAt  time.Time
}

func (AuditEvent) ModelMeta() models.Metadata {
	meta := modelMeta("AuditEvent", "blog_audit_event", "audit event", "audit events", []models.FieldMeta{
		{Name: "id", Column: "id", PrimaryKey: true},
		{Name: "actor", Column: "actor_id", RelationTarget: "auth.User", DeleteBehavior: "set_null"},
		{Name: "object_type", Column: "object_type"},
		{Name: "object_id", Column: "object_id"},
		{Name: "action", Column: "action"},
		{Name: "payload", Column: "payload"},
		{Name: "created_at", Column: "created_at"},
	})
	meta.Ordering = []string{"-created_at"}
	meta.Indexes = []models.Index{
		{Name: "blog_audit_object_idx", Fields: []models.IndexField{models.Asc("object_type"), models.Asc("object_id")}},
	}
	return meta
}

func ModelMetadata() []models.Metadata {
	return []models.Metadata{
		Author{}.ModelMeta(),
		Post{}.ModelMeta(),
		Tag{}.ModelMeta(),
		Comment{}.ModelMeta(),
		AuditEvent{}.ModelMeta(),
	}
}

func modelMeta(modelName string, tableName string, verboseName string, verbosePlural string, fields []models.FieldMeta) models.Metadata {
	return models.Metadata{
		AppLabel:           blogAppLabel,
		ModelName:          modelName,
		TableName:          tableName,
		DBTable:            tableName,
		VerboseName:        verboseName,
		VerboseNamePlural:  verbosePlural,
		DefaultManagerName: "objects",
		BaseManagerName:    "objects",
		DefaultPermissions: []string{"add", "change", "delete", "view"},
		GenerateMigrations: true,
		Fields:             fields,
	}
}
