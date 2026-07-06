package migrations

import (
	"encoding/json"
	"testing"

	"github.com/Newton-School/gogo/models"
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
			Unique:      true,
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
	if model.TableName != "blog_post" || len(model.Fields) != 2 || len(model.Indexes) != 3 || len(model.Constraints) != 1 {
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
	if model.Indexes[0].Name != "idx_title" || model.Indexes[0].Source != "model" || !model.Indexes[0].Unique || model.Indexes[0].Method != "gin" || model.Indexes[0].ConditionSQL != "deleted_at IS NULL" || model.Indexes[0].Expressions[0] != "LOWER(title)" || model.Indexes[0].Include[0] != "id" || model.Indexes[0].OpClasses[0] != "gin_trgm_ops" {
		t.Fatalf("explicit index state = %#v", model.Indexes[0])
	}
	if model.Indexes[1].Name != "uniq_title" || model.Indexes[1].Source != "constraint" || !model.Indexes[1].Unique || model.Indexes[1].ConditionSQL != "deleted_at IS NULL" || model.Indexes[1].Expressions[0] != "LOWER(title)" || model.Indexes[1].Include[0] != "id" || model.Indexes[1].OpClasses[0] != "text_pattern_ops" {
		t.Fatalf("constraint-derived unique index state = %#v", model.Indexes[1])
	}
	if model.Indexes[2].Fields[0] != "title" || model.Indexes[2].Name == "" || model.Indexes[2].Source != "field" {
		t.Fatalf("field-derived index state = %#v", model.Indexes[2])
	}
	if model.Constraints[0].Type != "unique" || model.Constraints[0].Fields[0] != "title" || model.Constraints[0].Name == "" || model.Constraints[0].Source != "field" {
		t.Fatalf("field-derived unique constraint state = %#v", model.Constraints[0])
	}
}

func TestStateFromRegistryConvertsPartialUniqueConstraintToUniqueIndex(t *testing.T) {
	registry := models.NewRegistry()
	if err := registry.RegisterMetadata(models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "title", Column: "title", Kind: "text"},
			{Name: "deleted_at", Column: "deleted_at", Kind: "timestamptz", Null: true},
		},
		Constraints: []models.Constraint{
			models.UniqueExpression("uniq_blog_post_lower_title", "LOWER(title)").
				WithCondition("deleted_at IS NULL"),
		},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	state := StateFromRegistry(registry)
	post := state.Models["blog.Post"]
	if len(post.Indexes) != 1 || !post.Indexes[0].Unique || post.Indexes[0].Source != "constraint" {
		t.Fatalf("indexes = %#v, want one constraint-backed unique index", post.Indexes)
	}
	if len(post.Constraints) != 0 {
		t.Fatalf("constraints = %#v, want no table constraint", post.Constraints)
	}
}

func TestStateFromRegistryPreservesSimpleUniqueDeferrableConstraint(t *testing.T) {
	registry := models.NewRegistry()
	if err := registry.RegisterMetadata(models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "slug", Column: "slug", Kind: "text"},
		},
		Constraints: []models.Constraint{
			models.Unique("uniq_blog_post_slug", "slug").WithDeferrable(models.DeferrableDeferred),
		},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	state := StateFromRegistry(registry)
	constraint := state.Models["blog.Post"].Constraints[0]
	if !constraint.Deferrable || !constraint.InitiallyDeferred {
		t.Fatalf("constraint deferrability = %#v", constraint)
	}
}

func TestIndexStateCloneAndSchemaPreserveUnique(t *testing.T) {
	state := NewProjectState()
	state.AddModel(ModelState{
		AppLabel:  "blog",
		Name:      "Post",
		TableName: "blog_post",
		Fields:    []FieldState{{Name: "id", Column: "id", PrimaryKey: true}},
		Indexes: []IndexState{{
			Name:        "uniq_blog_post_lower_title",
			Unique:      true,
			Expressions: []string{"LOWER(title)"},
		}},
	})

	cloned := state.Clone()
	clonedIndex := cloned.Models["blog.Post"].Indexes[0]
	clonedIndex.Unique = false
	cloned.Models["blog.Post"].Indexes[0] = clonedIndex

	original := state.Models["blog.Post"].Indexes[0]
	if !original.Unique {
		t.Fatalf("Clone() changed original unique index state: %#v", original)
	}

	schema := IndexSchemaFromState(original)
	if !schema.Unique {
		t.Fatalf("IndexSchemaFromState() dropped unique flag: %#v", schema)
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

func TestProjectStateFromRegistryPreservesRelationTargetFieldName(t *testing.T) {
	registry := models.NewRegistry()
	for _, meta := range []models.Metadata{
		{
			AppLabel:  "accounts",
			ModelName: "User",
			TableName: "accounts_user",
			Fields: []models.FieldMeta{
				{Name: "uid", Column: "uid", Kind: "uuid", PrimaryKey: true},
			},
		},
		{
			AppLabel:  "docs",
			ModelName: "Document",
			TableName: "docs_document",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
				{Name: "owner", Column: "owner_uid", Kind: "uuid", RelationTarget: "accounts.User", TargetFieldName: "uid", DeleteBehavior: "cascade"},
			},
		},
	} {
		if err := registry.RegisterMetadata(meta); err != nil {
			t.Fatalf("RegisterMetadata(%s.%s) error = %v", meta.AppLabel, meta.ModelName, err)
		}
	}

	state := StateFromRegistry(registry)
	field := state.Models["docs.Document"].Fields[1]
	if field.TargetFieldName != "uid" {
		t.Fatalf("TargetFieldName = %q, want uid", field.TargetFieldName)
	}
}

func TestStateFromRegistryGeneratesForeignKeyConstraintFromRelation(t *testing.T) {
	registry := models.NewRegistry()
	user := models.Metadata{
		AppLabel:  "accounts",
		ModelName: "User",
		TableName: "accounts_user",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true},
		},
	}
	post := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true},
			{Name: "author", Column: "author_id", Kind: "uuid", RelationTarget: "accounts.User", DeleteBehavior: "cascade"},
		},
	}
	for _, meta := range []models.Metadata{user, post} {
		if err := registry.RegisterMetadata(meta); err != nil {
			t.Fatalf("RegisterMetadata(%s) error = %v", meta.Label(), err)
		}
	}

	state := StateFromRegistry(registry)
	constraints := state.Models["blog.Post"].Constraints
	if len(constraints) != 1 {
		t.Fatalf("constraints = %#v, want one relation FK", constraints)
	}
	fk := constraints[0]
	if fk.Type != "foreign_key" || fk.Fields[0] != "author_id" || fk.ReferencesTable != "accounts_user" || fk.ReferencesColumns[0] != "id" || fk.OnDelete != "CASCADE" || fk.Source != "relation" {
		t.Fatalf("foreign key constraint = %#v", fk)
	}
}

func TestStateFromRegistryGeneratesForeignKeyToExplicitTargetField(t *testing.T) {
	registry := models.NewRegistry()
	for _, meta := range []models.Metadata{
		{
			AppLabel:  "accounts",
			ModelName: "User",
			TableName: "accounts_user",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
				{Name: "uid", Column: "external_uid", Kind: "uuid", Unique: true},
			},
		},
		{
			AppLabel:  "blog",
			ModelName: "Post",
			TableName: "blog_post",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
				{Name: "owner", Column: "owner_uid", Kind: "uuid", RelationTarget: "accounts.User", TargetFieldName: "uid", DeleteBehavior: "protect"},
			},
		},
	} {
		if err := registry.RegisterMetadata(meta); err != nil {
			t.Fatalf("RegisterMetadata(%s) error = %v", meta.Label(), err)
		}
	}

	state := StateFromRegistry(registry)
	fk := state.Models["blog.Post"].Constraints[0]
	if fk.ReferencesColumns[0] != "external_uid" || fk.OnDelete != "RESTRICT" {
		t.Fatalf("target-field foreign key = %#v", fk)
	}
}

func TestRelationDeleteBehaviorActions(t *testing.T) {
	defaultValue := models.DefaultSQL("0")
	cases := map[string]struct {
		field models.FieldMeta
		want  string
		ok    bool
	}{
		"cascade":     {field: models.FieldMeta{DeleteBehavior: "cascade"}, want: "CASCADE", ok: true},
		"restrict":    {field: models.FieldMeta{DeleteBehavior: "restrict"}, want: "RESTRICT", ok: true},
		"protect":     {field: models.FieldMeta{DeleteBehavior: "protect"}, want: "RESTRICT", ok: true},
		"set_null":    {field: models.FieldMeta{DeleteBehavior: "set_null", Null: true}, want: "SET NULL", ok: true},
		"set_default": {field: models.FieldMeta{DeleteBehavior: "set_default", DBDefault: defaultValue}, want: "SET DEFAULT", ok: true},
		"do_nothing":  {field: models.FieldMeta{DeleteBehavior: "do_nothing"}, want: "NO ACTION", ok: true},
		"set_value":   {field: models.FieldMeta{DeleteBehavior: "set_value"}, want: "NO ACTION", ok: true},
		"invalid":     {field: models.FieldMeta{DeleteBehavior: "delete_everything"}, ok: false},
	}
	for name, tc := range cases {
		got, ok := relationDeleteAction(tc.field)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%s relationDeleteAction() = (%q, %v), want (%q, %v)", name, got, ok, tc.want, tc.ok)
		}
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
