package migrations

import (
	"encoding/json"
	"testing"

	"github.com/cybersaksham/gogo/models"
)

func TestProjectStateCloneAndMutation(t *testing.T) {
	state := NewProjectState()
	state.AddModel(ModelState{
		AppLabel:  "blog",
		Name:      "Post",
		TableName: "blog_post",
		Fields:    []FieldState{{Name: "id", Column: "id", PrimaryKey: true}},
	})
	cloned := state.Clone()
	cloned.AddField("blog", "Post", FieldState{Name: "title", Column: "title"})
	clonedModel := cloned.Models["blog.Post"]
	clonedModel.Indexes = append(clonedModel.Indexes, IndexState{Name: "idx_title", Fields: []string{"title"}})
	cloned.Models["blog.Post"] = clonedModel

	if len(state.Models["blog.Post"].Fields) != 1 || len(state.Models["blog.Post"].Indexes) != 0 {
		t.Fatalf("original state was mutated: %#v", state.Models["blog.Post"])
	}
	if len(cloned.Models["blog.Post"].Fields) != 2 {
		t.Fatalf("cloned state missing mutation: %#v", cloned.Models["blog.Post"])
	}
}

func TestProjectStateFromRegistry(t *testing.T) {
	registry := models.NewRegistry()
	err := registry.RegisterMetadata(models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title", Kind: "char", ColumnTypes: map[string]string{"postgres": "varchar(200)"}, Null: true, Unique: true, DBIndex: true, DBDefault: models.DefaultValue("untitled"), DBCollation: "en_US"},
		},
		Indexes: []models.Index{{
			Name:        "idx_title",
			Fields:      []models.IndexField{models.Asc("title")},
			Expressions: []string{"LOWER(title)"},
			Method:      "gin",
			OpClasses:   []string{"gin_trgm_ops"},
			Include:     []string{"id"},
			Condition:   "deleted_at IS NULL",
		}},
		Constraints: []models.Constraint{{
			Name:        "uniq_title",
			Type:        models.ConstraintUnique,
			Fields:      []models.IndexField{models.Asc("title")},
			Expressions: []string{"LOWER(title)"},
			Condition:   "deleted_at IS NULL",
			Include:     []string{"id"},
			OpClasses:   []string{"text_pattern_ops"},
		}},
	})
	if err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	state := StateFromRegistry(registry)
	model := state.Models["blog.Post"]
	if model.TableName != "blog_post" || len(model.Fields) != 2 || len(model.Indexes) != 2 || len(model.Constraints) != 2 {
		t.Fatalf("registry state model = %#v", model)
	}
	if model.Fields[0].Name != "id" || !model.Fields[0].PrimaryKey {
		t.Fatalf("field state = %#v", model.Fields)
	}
	title := model.Fields[1]
	if title.Kind != "char" || title.ColumnTypes["postgres"] != "varchar(200)" || !title.Null || !title.Unique || !title.DBIndex || title.DBDefault == nil || title.DBDefault.Kind != models.DefaultLiteral || title.DBDefault.Value != "untitled" || title.DBCollation != "en_US" {
		t.Fatalf("rich field state was not preserved: %#v", title)
	}
	title.ColumnTypes["postgres"] = "text"
	again := StateFromRegistry(registry).Models["blog.Post"].Fields[1]
	if again.ColumnTypes["postgres"] != "varchar(200)" {
		t.Fatalf("field ColumnTypes state was not cloned: %#v", again.ColumnTypes)
	}
	if model.Indexes[0].Name != "idx_title" || model.Indexes[0].Source != "model" || model.Indexes[0].Method != "gin" || model.Indexes[0].ConditionSQL != "deleted_at IS NULL" || model.Indexes[0].Expressions[0] != "LOWER(title)" || model.Indexes[0].Include[0] != "id" || model.Indexes[0].OpClasses[0] != "gin_trgm_ops" {
		t.Fatalf("explicit index state = %#v", model.Indexes[0])
	}
	if model.Indexes[1].Fields[0] != "title" || model.Indexes[1].Name == "" || model.Indexes[1].Source != "field" {
		t.Fatalf("field-derived index state = %#v", model.Indexes[1])
	}
	if model.Constraints[0].Name != "uniq_title" || model.Constraints[0].Source != "model" || model.Constraints[0].ConditionSQL != "deleted_at IS NULL" || model.Constraints[0].Expressions[0] != "LOWER(title)" || model.Constraints[0].Include[0] != "id" || model.Constraints[0].OpClasses[0] != "text_pattern_ops" {
		t.Fatalf("explicit constraint state = %#v", model.Constraints[0])
	}
	if model.Constraints[1].Type != "unique" || model.Constraints[1].Fields[0] != "title" || model.Constraints[1].Name == "" || model.Constraints[1].Source != "field" {
		t.Fatalf("field-derived unique constraint state = %#v", model.Constraints[1])
	}
}

func TestProjectStateFromRegistryInfersRelationKindFromTargetPrimaryKey(t *testing.T) {
	registry := models.NewRegistry()
	for _, meta := range []models.Metadata{
		{
			AppLabel:  "accounts",
			ModelName: "User",
			TableName: "accounts_user",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true},
			},
		},
		{
			AppLabel:  "docs",
			ModelName: "Document",
			TableName: "docs_document",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
				{Name: "owner", Column: "owner_id", RelationTarget: "accounts.User", DeleteBehavior: "cascade"},
			},
		},
	} {
		if err := registry.RegisterMetadata(meta); err != nil {
			t.Fatalf("RegisterMetadata(%s.%s) error = %v", meta.AppLabel, meta.ModelName, err)
		}
	}

	state := StateFromRegistry(registry)
	field := state.Models["docs.Document"].Fields[1]
	if field.Kind != "uuid" {
		t.Fatalf("relation field kind = %q, want uuid", field.Kind)
	}
}

func TestFieldStateDatabaseDefaultManifestCompatibility(t *testing.T) {
	var legacy FieldState
	if err := json.Unmarshal([]byte(`{"name":"status","db_default":"draft"}`), &legacy); err != nil {
		t.Fatalf("legacy default unmarshal error = %v", err)
	}
	if legacy.DBDefault == nil || legacy.DBDefault.Kind != models.DefaultLiteral || legacy.DBDefault.Value != "draft" {
		t.Fatalf("legacy default = %#v", legacy.DBDefault)
	}

	var expression FieldState
	if err := json.Unmarshal([]byte(`{"name":"id","db_default":{"kind":"expression","sql":"gen_random_uuid()"}}`), &expression); err != nil {
		t.Fatalf("expression default unmarshal error = %v", err)
	}
	if expression.DBDefault == nil || expression.DBDefault.Kind != models.DefaultExpression || expression.DBDefault.SQL != "gen_random_uuid()" {
		t.Fatalf("expression default = %#v", expression.DBDefault)
	}

	data, err := json.Marshal(FieldState{Name: "status", DBDefault: databaseDefaultPtr(models.DefaultValue("draft"))})
	if err != nil {
		t.Fatalf("marshal default error = %v", err)
	}
	if string(data) != `{"name":"status","db_default":{"kind":"literal","value":"draft"}}` {
		t.Fatalf("marshaled default = %s", data)
	}
}

func TestProjectStateFromRegistrySkipsUnmanagedModels(t *testing.T) {
	managed := false
	registry := models.NewRegistry()
	if err := registry.RegisterMetadata(models.Metadata{
		AppLabel:  "legacy",
		ModelName: "Order",
		TableName: "legacy_order",
		Managed:   &managed,
		Fields:    []models.FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	state := StateFromRegistry(registry)
	if _, exists := state.Models["legacy.Order"]; exists {
		t.Fatalf("unmanaged model was included in migration state: %#v", state.Models)
	}
}

func databaseDefaultPtr(defaultValue models.DatabaseDefault) *models.DatabaseDefault {
	return &defaultValue
}
