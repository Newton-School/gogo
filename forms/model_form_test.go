package forms

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cybersaksham/gogo/models"
	modelfields "github.com/cybersaksham/gogo/models/fields"
)

type modelFormArticle struct {
	models.BaseModel
	Title     string
	Email     string
	Age       int64
	Published bool
	Status    string
	Slug      string
}

func (m *modelFormArticle) ModelMeta() models.Metadata {
	return modelFormArticleMeta()
}

func modelFormArticleMeta() models.Metadata {
	return models.Metadata{
		AppLabel:  "blog",
		ModelName: "Article",
		Fields: []models.FieldMeta{
			{Name: "id", PrimaryKey: true},
			{Name: "title"},
			{Name: "email"},
			{Name: "age"},
			{Name: "published"},
			{Name: "status"},
			{Name: "slug"},
		},
	}
}

func modelFormArticleFields() []modelfields.Field {
	return []modelfields.Field{
		modelfields.NewBigAutoField(modelfields.Options{Name: "id"}),
		modelfields.NewCharField(modelfields.Options{Name: "title", VerboseName: "article title", HelpText: "Shown publicly"}, 80),
		modelfields.NewEmailField(modelfields.Options{Name: "email"}, 254),
		modelfields.NewIntegerField(modelfields.Options{Name: "age"}),
		modelfields.NewBooleanField(modelfields.Options{Name: "published"}),
		modelfields.NewTextField(modelfields.Options{
			Name: "status",
			Choices: []modelfields.Choice{
				{Value: "draft", Label: "Draft"},
				{Value: "live", Label: "Live"},
			},
		}),
		modelfields.NewSlugField(modelfields.Options{Name: "slug"}, 80),
	}
}

func TestModelFormGeneratesFieldsAndAppliesOptions(t *testing.T) {
	article := &modelFormArticle{Title: "Old", Age: 4, Published: false, Status: "draft", Slug: "old-slug"}
	form := NewModelForm(ModelFormOptions{
		Model:           article,
		ModelFields:     modelFormArticleFields(),
		Include:         []string{"title", "email", "age", "published", "status", "slug"},
		Exclude:         []string{"slug"},
		Labels:          map[string]string{"title": "Headline"},
		HelpTexts:       map[string]string{"title": "Custom help"},
		Widgets:         map[string]Widget{"title": Textarea()},
		ReadOnly:        []string{"published"},
		LocalizedFields: []string{"age"},
		FieldClasses: map[string]ModelFieldFactory{
			"status": func(modelField modelfields.Field, options FieldOptions) *Field {
				return ChoiceField(options, []Choice{{Value: "draft", Label: "Draft"}, {Value: "live", Label: "Live"}})
			},
		},
		Data: map[string]any{
			"title":     "New",
			"email":     "dev@example.com",
			"age":       "7",
			"published": true,
			"status":    "live",
			"slug":      "ignored",
		},
	})

	if got := form.FieldNames(); !reflect.DeepEqual(got, []string{"title", "email", "age", "published", "status"}) {
		t.Fatalf("FieldNames() = %#v", got)
	}
	title := form.BoundField("title")
	if title.Field.Options.Label != "Headline" || title.Field.Options.HelpText != "Custom help" {
		t.Fatalf("title options = %#v", title.Field.Options)
	}
	if _, ok := title.Field.Options.Widget.(Widget); !ok {
		t.Fatalf("title widget override was not applied: %#v", title.Field.Options.Widget)
	}
	published := form.BoundField("published")
	if !published.Field.Options.Disabled || published.Field.Options.Initial != false {
		t.Fatalf("readonly published options = %#v", published.Field.Options)
	}
	if !form.BoundField("age").Field.Options.Localize {
		t.Fatalf("age field should be localized")
	}
	if form.BoundField("slug").Field != nil {
		t.Fatalf("excluded slug field should not exist")
	}
	if !form.IsValid() {
		t.Fatalf("IsValid() = false; errors=%#v non-field=%#v", form.Errors(), form.NonFieldErrors())
	}
	if got := form.CleanedData["status"]; got != "live" {
		t.Fatalf("custom field factory cleaned status = %#v", got)
	}
}

func TestModelFormSaveCommitFalseAndTrue(t *testing.T) {
	article := &modelFormArticle{Slug: "keep-me"}
	store := &recordingModelFormStore{}
	form := NewModelForm(ModelFormOptions{
		Model:       article,
		ModelFields: modelFormArticleFields(),
		Exclude:     []string{"slug"},
		Store:       store,
		Data: map[string]any{
			"title":     "Saved",
			"email":     "save@example.com",
			"age":       "9",
			"published": "true",
			"status":    "draft",
		},
	})

	saved, err := form.Save(false)
	if err != nil {
		t.Fatalf("Save(false) error = %v", err)
	}
	if saved != article {
		t.Fatalf("Save(false) returned %#v, want article", saved)
	}
	if store.saveCalls != 0 {
		t.Fatalf("Save(false) called store %d times", store.saveCalls)
	}
	if article.Title != "Saved" || article.Age != 9 || !article.Published || article.Slug != "keep-me" {
		t.Fatalf("article after Save(false) = %#v", article)
	}

	saved, err = form.Save(true, models.UpdateFields("title", "age"))
	if err != nil {
		t.Fatalf("Save(true) error = %v", err)
	}
	if saved != article || store.saveCalls != 1 {
		t.Fatalf("Save(true) = %#v calls=%d", saved, store.saveCalls)
	}
	if !reflect.DeepEqual(store.saveOptions.UpdateFields, []string{"title", "age"}) {
		t.Fatalf("UpdateFields = %#v", store.saveOptions.UpdateFields)
	}
}

func TestModelFormValidatesUniquenessHook(t *testing.T) {
	article := &uniqueModelFormArticle{}
	form := NewModelForm(ModelFormOptions{
		Model:       article,
		ModelFields: []modelfields.Field{modelfields.NewCharField(modelfields.Options{Name: "title"}, 80)},
		Data:        map[string]any{"title": "Taken"},
	})

	if form.IsValid() {
		t.Fatal("IsValid() = true, want uniqueness failure")
	}
	if !article.validateUniqueCalled {
		t.Fatal("ValidateUnique hook was not called")
	}
	if article.Title != "Taken" {
		t.Fatalf("model was not populated before uniqueness validation: %#v", article)
	}
	if got := form.NonFieldErrors().Messages(); !reflect.DeepEqual(got, []string{"duplicate title <bad>"}) {
		t.Fatalf("non-field uniqueness errors = %#v", got)
	}
}

func TestModelFormFallsBackToLightweightMetadata(t *testing.T) {
	editable := false
	form := NewModelForm(ModelFormOptions{
		Meta: models.Metadata{
			AppLabel:  "blog",
			ModelName: "Comment",
			Fields: []models.FieldMeta{
				{Name: "body", Kind: "text", Blank: true, VerboseName: "comment body", HelpText: "Shown publicly"},
				{Name: "status", Choices: []models.FieldChoiceMeta{{Value: "draft", Label: "Draft"}, {Value: "live", Label: "Live"}}, Default: "draft"},
				{Name: "hidden_note", Editable: &editable},
				{Name: "author_id", RelationTarget: "auth.User"},
			},
		},
		Data: map[string]any{"body": "", "status": "live", "hidden_note": "ignored", "author_id": "1"},
	})

	if got := form.FieldNames(); !reflect.DeepEqual(got, []string{"body", "status", "author_id"}) {
		t.Fatalf("FieldNames() = %#v", got)
	}
	body := form.BoundField("body").Field
	if body.Kind != "char" || body.Options.Required || body.Options.Label != "comment body" || body.Options.HelpText != "Shown publicly" {
		t.Fatalf("body fallback field = %#v", body)
	}
	status := form.BoundField("status").Field
	if status.Kind != "choice" || status.Options.Initial != "draft" || len(status.Choices) != 2 || status.Choices[1].Label != "Live" {
		t.Fatalf("status fallback field = %#v", status)
	}
	if form.BoundField("hidden_note").Field != nil {
		t.Fatalf("non-editable metadata field should be skipped")
	}
	if form.BoundField("author_id").Field.Kind != "model_choice" {
		t.Fatalf("author_id fallback kind = %q", form.BoundField("author_id").Field.Kind)
	}
	if !form.IsValid() {
		t.Fatalf("metadata-only form should validate; errors=%#v", form.Errors())
	}
	if form.CleanedData["status"] != "live" {
		t.Fatalf("status cleaned value = %#v", form.CleanedData["status"])
	}
}

type uniqueModelFormArticle struct {
	models.BaseModel
	Title                string
	validateUniqueCalled bool
}

func (m *uniqueModelFormArticle) ModelMeta() models.Metadata {
	return models.Metadata{AppLabel: "blog", ModelName: "UniqueArticle", Fields: []models.FieldMeta{{Name: "title"}}}
}

func (m *uniqueModelFormArticle) ValidateUnique(context.Context) error {
	m.validateUniqueCalled = true
	if m.Title == "Taken" {
		return errors.New("duplicate title <bad>")
	}
	return nil
}

type recordingModelFormStore struct {
	saveCalls   int
	saveOptions models.SaveOptions
}

func (s *recordingModelFormStore) Save(_ context.Context, _ models.Model, options models.SaveOptions) error {
	s.saveCalls++
	s.saveOptions = options
	return nil
}

func (s *recordingModelFormStore) Delete(context.Context, models.Model, models.DeleteOptions) error {
	return nil
}

func (s *recordingModelFormStore) RefreshFromDB(context.Context, models.Model, models.RefreshOptions) error {
	return nil
}

func (s *recordingModelFormStore) FromDB(context.Context, models.Model, map[string]any) error {
	return nil
}
