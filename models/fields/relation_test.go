package fields

import (
	"errors"
	"testing"

	"github.com/cybersaksham/gogo/models"
)

func TestRelationshipFieldTypesAndMetadata(t *testing.T) {
	fk := NewForeignKey(Options{Name: "author", Null: true}, RelationConfig{
		Target:           "auth.User",
		OnDelete:         SetNull,
		RelatedName:      "articles",
		RelatedQueryName: "article",
	})
	if fk.RelationType() != RelationForeignKey || fk.Target() != "auth.User" {
		t.Fatalf("foreign key metadata = %#v", fk)
	}
	if fk.RelatedName() != "articles" || fk.RelatedQueryName() != "article" {
		t.Fatalf("related metadata = (%q, %q)", fk.RelatedName(), fk.RelatedQueryName())
	}

	one := NewOneToOneField(Options{Name: "profile"}, RelationConfig{Target: "auth.User", OnDelete: Cascade})
	if one.RelationType() != RelationOneToOne {
		t.Fatalf("one-to-one type = %s", one.RelationType())
	}

	many := NewManyToManyField(Options{Name: "tags"}, RelationConfig{Target: "blog.Tag", Through: "blog.ArticleTag"})
	if many.RelationType() != RelationManyToMany || many.Through() != "blog.ArticleTag" {
		t.Fatalf("many-to-many metadata = %#v", many)
	}
}

func TestForeignKeySupportsExplicitUUIDColumnType(t *testing.T) {
	fk := NewForeignKey(Options{Name: "owner", Column: "owner_id"}, RelationConfig{
		Target:      "accounts.User",
		OnDelete:    Cascade,
		ColumnTypes: map[string]string{"postgres": "uuid", "sqlite": "text"},
	})
	if got := fk.ColumnType("postgres"); got != "uuid" {
		t.Fatalf("foreign key postgres column type = %q, want uuid", got)
	}
	if got := fk.ColumnType("sqlite"); got != "text" {
		t.Fatalf("foreign key sqlite column type = %q, want text", got)
	}
	meta := Metadata(fk, "postgres")
	if meta.Kind != "uuid" || meta.RelationTarget != "accounts.User" || meta.DeleteBehavior != string(Cascade) {
		t.Fatalf("relation metadata = %#v", meta)
	}
}

func TestForeignKeyPreservesTargetFieldNameInMetadata(t *testing.T) {
	fk := NewForeignKey(Options{Name: "owner", Column: "owner_uid"}, RelationConfig{
		Target:          "accounts.User",
		TargetFieldName: "uid",
		OnDelete:        Cascade,
		ColumnTypes:     map[string]string{"postgres": "uuid"},
	})

	meta := Metadata(fk, "postgres")
	if meta.TargetFieldName != "uid" {
		t.Fatalf("TargetFieldName = %q, want uid", meta.TargetFieldName)
	}
}

func TestRelationshipFieldsSupportSelfAndLazyReferences(t *testing.T) {
	self := NewForeignKey(Options{Name: "parent", Null: true}, RelationConfig{Target: Self, OnDelete: SetNull})
	if !self.IsSelfReference() {
		t.Fatalf("self relationship not detected")
	}
	lazy := NewForeignKey(Options{Name: "owner"}, RelationConfig{Target: "auth.User", OnDelete: Cascade})
	if lazy.Target() != "auth.User" {
		t.Fatalf("lazy target = %q, want auth.User", lazy.Target())
	}
}

func TestRelationshipDeleteBehaviors(t *testing.T) {
	tests := []RelationConfig{
		{Target: "auth.User", OnDelete: Cascade},
		{Target: "auth.User", OnDelete: Protect},
		{Target: "auth.User", OnDelete: Restrict},
		{Target: "auth.User", OnDelete: SetNull},
		{Target: "auth.User", OnDelete: SetDefault},
		{Target: "auth.User", OnDelete: SetValue, SetValue: int64(1)},
		{Target: "auth.User", OnDelete: DoNothing},
	}

	for _, config := range tests {
		options := Options{Name: "user", Null: true, Default: int64(1)}
		field := NewForeignKey(options, config)
		if field.OnDelete() != config.OnDelete {
			t.Fatalf("OnDelete() = %s, want %s", field.OnDelete(), config.OnDelete)
		}
	}
}

func TestValidateRelationsRejectsMissingTargetsInvalidThroughAndDuplicateReverseNames(t *testing.T) {
	registry := relationRegistry(t)
	missing := NewForeignKey(Options{Name: "missing"}, RelationConfig{Target: "auth.Missing", OnDelete: Cascade})
	if err := ValidateRelations(registry, missing); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("missing target error = %v, want ErrInvalidField", err)
	}

	invalidThrough := NewManyToManyField(Options{Name: "tags"}, RelationConfig{Target: "blog.Tag", Through: "blog.Missing"})
	if err := ValidateRelations(registry, invalidThrough); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("invalid through error = %v, want ErrInvalidField", err)
	}

	first := NewForeignKey(Options{Name: "author"}, RelationConfig{Target: "auth.User", OnDelete: Cascade, RelatedName: "articles"})
	second := NewForeignKey(Options{Name: "editor"}, RelationConfig{Target: "auth.User", OnDelete: Cascade, RelatedName: "articles"})
	if err := ValidateRelations(registry, first, second); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("duplicate reverse error = %v, want ErrInvalidField", err)
	}
}

func TestValidateRelationsRejectsInvalidDeleteBehaviorConfiguration(t *testing.T) {
	registry := relationRegistry(t)

	setNull := NewForeignKey(Options{Name: "author"}, RelationConfig{Target: "auth.User", OnDelete: SetNull})
	if err := ValidateRelations(registry, setNull); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("set null error = %v, want ErrInvalidField", err)
	}

	setDefault := NewForeignKey(Options{Name: "author"}, RelationConfig{Target: "auth.User", OnDelete: SetDefault})
	if err := ValidateRelations(registry, setDefault); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("set default error = %v, want ErrInvalidField", err)
	}
}

func relationRegistry(t *testing.T) *models.Registry {
	t.Helper()
	registry := models.NewRegistry()
	for _, meta := range []models.Metadata{
		{AppLabel: "auth", ModelName: "User"},
		{AppLabel: "blog", ModelName: "Tag"},
		{AppLabel: "blog", ModelName: "ArticleTag"},
	} {
		if err := registry.RegisterMetadata(models.ResolveMetadata(staticModel{meta: meta})); err != nil {
			t.Fatalf("RegisterMetadata(%s.%s) error = %v", meta.AppLabel, meta.ModelName, err)
		}
	}
	return registry
}

type staticModel struct {
	models.BaseModel
	meta models.Metadata
}

func (m staticModel) ModelMeta() models.Metadata {
	return m.meta
}
