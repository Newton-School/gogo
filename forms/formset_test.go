package forms

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/models"
	modelfields "github.com/Newton-School/gogo/models/fields"
)

func TestFormSetManagementAddEditDeleteOrderAndExtra(t *testing.T) {
	formset := NewFormSet(FormSetOptions{
		Prefix: "items",
		Data: map[string]any{
			"items-TOTAL_FORMS":   "4",
			"items-INITIAL_FORMS": "2",
			"items-MIN_NUM_FORMS": "1",
			"items-MAX_NUM_FORMS": "4",
			"items-0-title":       "Edited",
			"items-0-ORDER":       "2",
			"items-1-title":       "Deleted",
			"items-1-DELETE":      "on",
			"items-1-ORDER":       "1",
			"items-2-title":       "Added",
			"items-2-ORDER":       "3",
		},
		Initial: []map[string]any{
			{"title": "Original"},
			{"title": "Remove"},
		},
		Extra:       2,
		CanDelete:   true,
		CanOrder:    true,
		MinForms:    1,
		MaxForms:    4,
		FormFactory: titleFormFactory,
	})

	if management := formset.ManagementForm(); management.TotalForms != 4 || management.InitialForms != 2 || management.MinForms != 1 || management.MaxForms != 4 {
		t.Fatalf("management form = %#v", management)
	}
	if !formset.IsValid() {
		t.Fatalf("IsValid() = false; errors=%#v", formset.NonFormErrors())
	}
	if len(formset.Forms()) != 4 {
		t.Fatalf("Forms() length = %d, want 4", len(formset.Forms()))
	}
	if len(formset.InitialForms()) != 2 || len(formset.NewForms()) != 2 {
		t.Fatalf("initial/new lengths = %d/%d", len(formset.InitialForms()), len(formset.NewForms()))
	}
	if got := formset.DeletedForms(); len(got) != 1 || got[0].Index != 1 {
		t.Fatalf("DeletedForms() = %#v", got)
	}
	ordered := formset.OrderedForms()
	if got := []any{ordered[0].Form.BoundField("title").Value(), ordered[1].Form.BoundField("title").Value()}; !reflect.DeepEqual(got, []any{"Edited", "Added"}) {
		t.Fatalf("OrderedForms() titles = %#v", got)
	}
	if got := formset.EmptyForms(); len(got) != 1 || got[0].Index != 3 {
		t.Fatalf("EmptyForms() = %#v", got)
	}
}

func TestFormSetManagementMinAndMaxValidation(t *testing.T) {
	missing := NewFormSet(FormSetOptions{Prefix: "items", Data: map[string]any{}, FormFactory: titleFormFactory})
	if missing.IsValid() {
		t.Fatal("missing management form should be invalid")
	}
	if got := missing.NonFormErrors().Messages(); !reflect.DeepEqual(got, []string{"management form is missing TOTAL_FORMS"}) {
		t.Fatalf("missing management errors = %#v", got)
	}

	tooFew := NewFormSet(FormSetOptions{
		Prefix:      "items",
		Data:        managementData("items", 1, 0, 2, 3, map[string]any{"items-0-title": "Only"}),
		MinForms:    2,
		MaxForms:    3,
		FormFactory: titleFormFactory,
	})
	if tooFew.IsValid() {
		t.Fatal("too few forms should be invalid")
	}
	if got := tooFew.NonFormErrors().Messages(); !reflect.DeepEqual(got, []string{"Please submit at least 2 forms."}) {
		t.Fatalf("min errors = %#v", got)
	}

	tooMany := NewFormSet(FormSetOptions{
		Prefix: "items",
		Data: managementData("items", 3, 0, 0, 2, map[string]any{
			"items-0-title": "A",
			"items-1-title": "B",
			"items-2-title": "C",
		}),
		MaxForms:    2,
		FormFactory: titleFormFactory,
	})
	if tooMany.IsValid() {
		t.Fatal("too many forms should be invalid")
	}
	if got := tooMany.NonFormErrors().Messages(); !reflect.DeepEqual(got, []string{"Please submit at most 2 forms."}) {
		t.Fatalf("max errors = %#v", got)
	}
}

func TestInlineFormSetLinksParentAndSavesChildren(t *testing.T) {
	parent := &inlineParent{ID: 10}
	store := &recordingModelFormStore{}
	inline := NewInlineFormSet(InlineFormSetOptions{
		Parent:        parent,
		RelationField: "parent",
		ModelFactory: func(int) models.Model {
			return &inlineChild{}
		},
		ModelFields: []modelfields.Field{
			modelfields.NewCharField(modelfields.Options{Name: "title"}, 80),
		},
		Store: store,
		FormSetOptions: FormSetOptions{
			Prefix: "children",
			Data:   managementData("children", 1, 0, 0, 1, map[string]any{"children-0-title": "Child"}),
			Extra:  1,
		},
	})

	children, err := inline.Save(false)
	if err != nil {
		t.Fatalf("Save(false) error = %v", err)
	}
	if store.saveCalls != 0 {
		t.Fatalf("Save(false) store calls = %d", store.saveCalls)
	}
	child := children[0].(*inlineChild)
	if child.Title != "Child" || child.Parent != parent {
		t.Fatalf("inline child = %#v", child)
	}

	if _, err := inline.Save(true); err != nil {
		t.Fatalf("Save(true) error = %v", err)
	}
	if store.saveCalls != 1 {
		t.Fatalf("Save(true) store calls = %d, want 1", store.saveCalls)
	}
}

func titleFormFactory(options FormSetFormOptions) *Form {
	return NewForm(FormOptions{
		Fields: map[string]*Field{
			"title": CharField(FieldOptions{Required: true}),
		},
		FieldOrder: []string{"title"},
		Data:       options.Data,
		Initial:    options.Initial,
	})
}

func managementData(prefix string, total, initial, min, max int, values map[string]any) map[string]any {
	data := map[string]any{
		prefix + "-TOTAL_FORMS":   total,
		prefix + "-INITIAL_FORMS": initial,
		prefix + "-MIN_NUM_FORMS": min,
		prefix + "-MAX_NUM_FORMS": max,
	}
	for key, value := range values {
		data[key] = value
	}
	return data
}

type inlineParent struct {
	models.BaseModel
	ID int64
}

func (m *inlineParent) ModelMeta() models.Metadata {
	return models.Metadata{AppLabel: "blog", ModelName: "Parent"}
}

type inlineChild struct {
	models.BaseModel
	Parent *inlineParent
	Title  string
}

func (m *inlineChild) ModelMeta() models.Metadata {
	return models.Metadata{AppLabel: "blog", ModelName: "Child", Fields: []models.FieldMeta{{Name: "title"}}}
}
