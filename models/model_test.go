package models

import "testing"

type articleModel struct {
	BaseModel
}

func (articleModel) ModelMeta() Metadata {
	return Metadata{
		AppLabel:  "blog",
		ModelName: "Article",
	}
}

type storyModel struct {
	BaseModel
}

func (storyModel) ModelMeta() Metadata {
	return Metadata{
		AppLabel:           "content",
		ModelName:          "Story",
		TableName:          "published_stories",
		VerboseName:        "story",
		VerboseNamePlural:  "stories",
		DefaultManagerName: "published",
	}
}

type translationModel struct {
	BaseModel
}

func (translationModel) ModelMeta() Metadata {
	return Metadata{
		AppLabel:  "content",
		ModelName: "Translation",
		CompositePrimaryKey: &CompositePrimaryKey{
			Columns: []string{"locale", "slug"},
		},
	}
}

func TestResolveMetadataAppliesDjangoStyleDefaults(t *testing.T) {
	meta := ResolveMetadata(articleModel{})

	if meta.TableName != "blog_article" {
		t.Fatalf("TableName = %q, want blog_article", meta.TableName)
	}
	if meta.VerboseName != "article" {
		t.Fatalf("VerboseName = %q, want article", meta.VerboseName)
	}
	if meta.VerboseNamePlural != "articles" {
		t.Fatalf("VerboseNamePlural = %q, want articles", meta.VerboseNamePlural)
	}
	if meta.DefaultManagerName != "objects" {
		t.Fatalf("DefaultManagerName = %q, want objects", meta.DefaultManagerName)
	}
}

func TestBaseModelZeroValueStateIsNew(t *testing.T) {
	var model BaseModel
	if model.ModelState() != StateNew {
		t.Fatalf("ModelState() = %s, want new", model.ModelState())
	}

	model.SetModelState(StateDirty)
	if model.ModelState() != StateDirty {
		t.Fatalf("ModelState() = %s, want dirty", model.ModelState())
	}
}

func TestResolveMetadataPreservesExplicitOptions(t *testing.T) {
	meta := ResolveMetadata(storyModel{})

	if meta.TableName != "published_stories" {
		t.Fatalf("TableName = %q, want published_stories", meta.TableName)
	}
	if meta.VerboseName != "story" {
		t.Fatalf("VerboseName = %q, want story", meta.VerboseName)
	}
	if meta.VerboseNamePlural != "stories" {
		t.Fatalf("VerboseNamePlural = %q, want stories", meta.VerboseNamePlural)
	}
	if meta.DefaultManagerName != "published" {
		t.Fatalf("DefaultManagerName = %q, want published", meta.DefaultManagerName)
	}
}

func TestCompositePrimaryKeyMetadataIsCopied(t *testing.T) {
	meta := ResolveMetadata(translationModel{})
	if meta.CompositePrimaryKey == nil {
		t.Fatalf("CompositePrimaryKey is nil")
	}
	if got := meta.CompositePrimaryKey.Columns; len(got) != 2 || got[0] != "locale" || got[1] != "slug" {
		t.Fatalf("Columns = %#v, want locale and slug", got)
	}

	meta.CompositePrimaryKey.Columns[0] = "changed"
	again := ResolveMetadata(translationModel{})
	if again.CompositePrimaryKey.Columns[0] != "locale" {
		t.Fatalf("CompositePrimaryKey was mutated across resolutions: %#v", again.CompositePrimaryKey.Columns)
	}
}

func TestMetadataCloneDeepCopiesFieldMetadata(t *testing.T) {
	meta := Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []FieldMeta{{
			Name:        "title",
			ColumnTypes: map[string]string{"postgres": "varchar(200)"},
			DBDefault:   "untitled",
			Editable:    boolPointer(false),
			Choices:     []FieldChoiceMeta{{Value: "draft", Label: "Draft"}},
			Validators:  []FieldValidator{func(any) error { return nil }},
		}},
	}

	cloned := meta.Clone()
	cloned.Fields[0].ColumnTypes["postgres"] = "text"
	cloned.Fields[0].DBDefault = "changed"
	*cloned.Fields[0].Editable = true
	cloned.Fields[0].Choices[0].Label = "Changed"
	cloned.Fields[0].Validators = append(cloned.Fields[0].Validators, func(any) error { return nil })

	if meta.Fields[0].ColumnTypes["postgres"] != "varchar(200)" {
		t.Fatalf("field ColumnTypes were not deep-copied: %#v", meta.Fields[0].ColumnTypes)
	}
	if meta.Fields[0].DBDefault != "untitled" {
		t.Fatalf("field DBDefault was mutated: %#v", meta.Fields[0].DBDefault)
	}
	if meta.Fields[0].Editable == nil || *meta.Fields[0].Editable {
		t.Fatalf("field Editable was mutated: %#v", meta.Fields[0].Editable)
	}
	if meta.Fields[0].Choices[0].Label != "Draft" {
		t.Fatalf("field Choices were not deep-copied: %#v", meta.Fields[0].Choices)
	}
	if len(meta.Fields[0].Validators) != 1 {
		t.Fatalf("field Validators were not deep-copied: %#v", meta.Fields[0].Validators)
	}
}

func boolPointer(value bool) *bool {
	return &value
}
