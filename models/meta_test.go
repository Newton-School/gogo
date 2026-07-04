package models

import (
	"errors"
	"reflect"
	"testing"
)

type metadataOptionsModel struct {
	BaseModel
}

func (metadataOptionsModel) ModelMeta() Metadata {
	managed := false
	return Metadata{
		DBTable:            "custom_table",
		DBTableComment:     "stores custom rows",
		AppLabel:           "custom_app",
		ModelName:          "MetadataOptions",
		VerboseName:        "metadata option",
		VerboseNamePlural:  "metadata options",
		Ordering:           []string{"-created_at", "name"},
		OrderWithRespectTo: "owner",
		GetLatestBy:        []string{"updated_at"},
		DefaultRelatedName: "metadata_options",
		DefaultManagerName: "objects",
		BaseManagerName:    "base_objects",
		Abstract:           true,
		Proxy:              true,
		Managed:            &managed,
		RequiredDBVendor:   "postgresql",
		RequiredDBFeatures: []string{"supports_json_field"},
		Indexes:            []Index{{Name: "idx_name", Fields: []IndexField{Asc("name")}}},
		Constraints:        []Constraint{{Name: "uniq_name", Type: ConstraintUnique, Fields: []IndexField{Asc("name")}}},
		Permissions:        []Permission{{CodeName: "publish", Name: "Can publish"}},
		DefaultPermissions: []string{"add", "change", "delete", "view"},
		SelectOnSave:       true,
		GenerateMigrations: false,
	}
}

func TestResolveMetadataPreservesEveryModelOption(t *testing.T) {
	meta := ResolveMetadata(metadataOptionsModel{})

	if meta.TableName != "custom_table" || meta.DBTable != "custom_table" {
		t.Fatalf("table names = (%q, %q), want custom_table", meta.TableName, meta.DBTable)
	}
	if meta.DBTableComment != "stores custom rows" {
		t.Fatalf("DBTableComment = %q", meta.DBTableComment)
	}
	if !reflect.DeepEqual(meta.Ordering, []string{"-created_at", "name"}) {
		t.Fatalf("Ordering = %#v", meta.Ordering)
	}
	if meta.OrderWithRespectTo != "owner" {
		t.Fatalf("OrderWithRespectTo = %q", meta.OrderWithRespectTo)
	}
	if !reflect.DeepEqual(meta.GetLatestBy, []string{"updated_at"}) {
		t.Fatalf("GetLatestBy = %#v", meta.GetLatestBy)
	}
	if meta.DefaultRelatedName != "metadata_options" || meta.BaseManagerName != "base_objects" {
		t.Fatalf("related/base manager = (%q, %q)", meta.DefaultRelatedName, meta.BaseManagerName)
	}
	if !meta.Abstract || !meta.Proxy || meta.IsManaged() || !meta.SelectOnSave {
		t.Fatalf("boolean options not preserved: %#v", meta)
	}
	if meta.RequiredDBVendor != "postgresql" || !reflect.DeepEqual(meta.RequiredDBFeatures, []string{"supports_json_field"}) {
		t.Fatalf("required db options = (%q, %#v)", meta.RequiredDBVendor, meta.RequiredDBFeatures)
	}
	if len(meta.Indexes) != 1 || meta.Indexes[0].Name != "idx_name" || meta.Indexes[0].Fields[0].Name != "name" {
		t.Fatalf("Indexes = %#v", meta.Indexes)
	}
	if len(meta.Constraints) != 1 || meta.Constraints[0].Type != ConstraintUnique {
		t.Fatalf("Constraints = %#v", meta.Constraints)
	}
	if len(meta.Permissions) != 1 || meta.Permissions[0].CodeName != "publish" {
		t.Fatalf("Permissions = %#v", meta.Permissions)
	}
	if !reflect.DeepEqual(meta.DefaultPermissions, []string{"add", "change", "delete", "view"}) {
		t.Fatalf("DefaultPermissions = %#v", meta.DefaultPermissions)
	}
}

func TestResolveMetadataCopiesOptionSlices(t *testing.T) {
	meta := ResolveMetadata(metadataOptionsModel{})
	meta.Indexes[0].Fields[0].Name = "changed"
	meta.RequiredDBFeatures[0] = "changed"

	again := ResolveMetadata(metadataOptionsModel{})
	if again.Indexes[0].Fields[0].Name != "name" || again.RequiredDBFeatures[0] != "supports_json_field" {
		t.Fatalf("metadata slices were mutated across resolutions: %#v", again)
	}
}

func TestValidateMetadataRejectsUnmanagedGeneratedMigrations(t *testing.T) {
	managed := false
	err := ValidateMetadata(Metadata{
		AppLabel:           "bad",
		ModelName:          "BadModel",
		Managed:            &managed,
		GenerateMigrations: true,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ValidateMetadata() error = %v, want ErrInvalidMetadata", err)
	}
}

func TestValidateMetadataRejectsDuplicateColumnsAndMissingPrimaryKey(t *testing.T) {
	err := ValidateMetadata(Metadata{
		AppLabel:  "bad",
		ModelName: "DuplicateColumn",
		Fields: []FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "legacy_id", Column: "id"},
		},
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ValidateMetadata(duplicate columns) error = %v, want ErrInvalidMetadata", err)
	}

	err = ValidateMetadata(Metadata{
		AppLabel:  "bad",
		ModelName: "MissingPrimaryKey",
		Fields:    []FieldMeta{{Name: "name", Column: "name"}},
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ValidateMetadata(missing primary key) error = %v, want ErrInvalidMetadata", err)
	}
}

func TestValidateMetadataRejectsInvalidManagedRelationDeleteBehavior(t *testing.T) {
	cases := []Metadata{
		{
			AppLabel:  "bad",
			ModelName: "SetNullNotNullable",
			Fields: []FieldMeta{
				{Name: "id", Column: "id", PrimaryKey: true},
				{Name: "owner", Column: "owner_id", RelationTarget: "auth.User", DeleteBehavior: "set_null"},
			},
		},
		{
			AppLabel:  "bad",
			ModelName: "SetDefaultNoDefault",
			Fields: []FieldMeta{
				{Name: "id", Column: "id", PrimaryKey: true},
				{Name: "owner", Column: "owner_id", RelationTarget: "auth.User", DeleteBehavior: "set_default"},
			},
		},
		{
			AppLabel:  "bad",
			ModelName: "UnknownDeleteAction",
			Fields: []FieldMeta{
				{Name: "id", Column: "id", PrimaryKey: true},
				{Name: "owner", Column: "owner_id", RelationTarget: "auth.User", DeleteBehavior: "delete_everything"},
			},
		},
	}
	for _, meta := range cases {
		if err := ValidateMetadata(meta); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("ValidateMetadata(%s) error = %v, want ErrInvalidMetadata", meta.ModelName, err)
		}
	}
}

func TestValidateMetadataRejectsDuplicateIndexesConstraintsAndPermissions(t *testing.T) {
	err := ValidateMetadata(Metadata{
		AppLabel:  "bad",
		ModelName: "DuplicateNames",
		Fields:    []FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}},
		Indexes: []Index{
			{Name: "idx_name", Fields: []IndexField{Asc("name")}},
			{Name: "idx_name", Fields: []IndexField{Asc("slug")}},
		},
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ValidateMetadata(duplicate indexes) error = %v, want ErrInvalidMetadata", err)
	}

	err = ValidateMetadata(Metadata{
		AppLabel:  "bad",
		ModelName: "DuplicateConstraints",
		Fields:    []FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}},
		Constraints: []Constraint{
			{Name: "uniq_name", Type: ConstraintUnique, Fields: []IndexField{Asc("name")}},
			{Name: "uniq_name", Type: ConstraintUnique, Fields: []IndexField{Asc("slug")}},
		},
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ValidateMetadata(duplicate constraints) error = %v, want ErrInvalidMetadata", err)
	}

	err = ValidateMetadata(Metadata{
		AppLabel:  "bad",
		ModelName: "DuplicatePermissions",
		Fields:    []FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}},
		Permissions: []Permission{
			{CodeName: "publish", Name: "Can publish"},
			{CodeName: "publish", Name: "Can publish again"},
		},
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ValidateMetadata(duplicate permissions) error = %v, want ErrInvalidMetadata", err)
	}
}
