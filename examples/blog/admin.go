package blog

import (
	"fmt"

	"github.com/Newton-School/gogo/admin"
)

func RegisterAdmin(registry *admin.Registry) error {
	if registry == nil {
		return fmt.Errorf("blog admin registry is required")
	}
	configs := map[string]admin.ModelAdmin{
		"blog.Author": {
			ListDisplay:       []string{"display_name", "website", "updated_at"},
			SearchFields:      []string{"display_name", "bio", "website"},
			ListSelectRelated: []string{"user"},
			ReadonlyFields:    []string{"created_at", "updated_at"},
			Fieldsets: []admin.Fieldset{
				{Name: "Profile", Fields: []string{"user", "display_name", "bio", "website"}},
				{Name: "System", Fields: []string{"created_at", "updated_at"}},
			},
		},
		"blog.Post": {
			ListDisplay:        []string{"title", "author", "status", "published_at", "updated_at"},
			ListDisplayLinks:   []string{"title"},
			ListFilter:         []string{"status", "published_at", "author"},
			ListEditable:       []string{"status"},
			SearchFields:       []string{"title", "slug", "body"},
			DateHierarchy:      "published_at",
			PrepopulatedFields: map[string][]string{"slug": []string{"title"}},
			RawIDFields:        []string{"author"},
			FilterHorizontal:   []string{"tags"},
			ReadonlyFields:     []string{"created_at", "updated_at"},
			Ordering:           []string{"-published_at", "-created_at"},
			Actions:            []string{"publish_selected", "archive_selected"},
			Fieldsets: []admin.Fieldset{
				{Name: "Content", Fields: []string{"title", "slug", "body", "tags"}},
				{Name: "Publishing", Fields: []string{"author", "status", "published_at"}},
				{Name: "System", Fields: []string{"created_at", "updated_at"}},
			},
			Inlines: []admin.Inline{
				{Model: "blog.Comment", Kind: admin.InlineTabular, Extra: 0, CanDelete: true, ShowChangeLink: true, FKName: "post"},
			},
		},
		"blog.Tag": {
			ListDisplay:        []string{"name", "slug"},
			SearchFields:       []string{"name", "slug"},
			PrepopulatedFields: map[string][]string{"slug": []string{"name"}},
		},
		"blog.Comment": {
			ListDisplay:       []string{"name", "email", "post", "status", "created_at"},
			ListFilter:        []string{"status", "created_at"},
			ListEditable:      []string{"status"},
			SearchFields:      []string{"name", "email", "body"},
			RawIDFields:       []string{"post"},
			ReadonlyFields:    []string{"created_at", "updated_at", "ip_address", "user_agent"},
			ListSelectRelated: []string{"post"},
			Actions:           []string{"approve_selected", "mark_as_spam"},
		},
		"blog.AuditEvent": {
			ListDisplay:     []string{"action", "object_type", "object_id", "actor", "created_at"},
			ListFilter:      []string{"action", "object_type", "created_at"},
			SearchFields:    []string{"object_type", "object_id", "action"},
			RawIDFields:     []string{"actor"},
			ReadonlyFields:  []string{"actor", "object_type", "object_id", "action", "payload", "created_at"},
			Ordering:        []string{"-created_at"},
			ActionsOnTop:    false,
			ActionsOnBottom: false,
		},
	}
	for _, meta := range ModelMetadata() {
		modelAdmin, ok := configs[meta.Label()]
		if !ok {
			continue
		}
		if err := modelAdmin.Validate(meta); err != nil {
			return err
		}
		if err := registry.RegisterMetadata(meta, modelAdmin); err != nil {
			return err
		}
	}
	return nil
}
