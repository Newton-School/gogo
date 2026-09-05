package forms

import (
	"context"
	"net/url"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

type relatedFormModel struct {
	models.Base
	ID   int64
	Name string
}

func (*relatedFormModel) Schema() models.Schema {
	return models.Schema{AppLabel: "test", Name: "Related", Fields: []models.Field{models.BigAutoField("ID"), models.CharField("Name"), models.ManyToManyField("tags", models.Relation{Target: "test.Sample"})}}
}

func TestModelFormRejectsChangedRelationResolutionBeforePersistence(t *testing.T) {
	record, _ := models.Bind(&relatedFormModel{Name: "Before"})
	resolvedID := int64(1)
	store := &formStore{}
	form, err := NewModelForm(context.Background(), record, ModelFormOptions{Fields: []string{"Name", "tags"}, Persistence: store, ResolveRelation: func(context.Context, models.Field, []string) ([]any, error) { return []any{resolvedID}, nil }}, WithData(url.Values{"Name": {"After"}, "tags": {"alias"}}))
	if err != nil || !form.IsValid() {
		t.Fatal(form, err)
	}
	resolvedID = 2
	if _, err := form.Save(true); err == nil {
		t.Fatal("saved a stale relation after alias changed")
	}
	for _, event := range store.events {
		if event == "parent" || event == "relations" {
			t.Fatal("wrote before identity check", store.events)
		}
	}
}

func TestRelationIdentitySnapshotDoesNotAliasResolvedModel(t *testing.T) {
	record, _ := models.Bind(&relatedFormModel{Name: "Before"})
	target, _ := models.Bind(&modelSample{ID: 1, Name: "Target", Role: "member"})
	store := &formStore{}
	form, err := NewModelForm(context.Background(), record, ModelFormOptions{Fields: []string{"Name", "tags"}, Persistence: store, ResolveRelation: func(context.Context, models.Field, []string) ([]any, error) { return []any{target}, nil }}, WithData(url.Values{"Name": {"After"}, "tags": {"1"}}))
	if err != nil || !form.IsValid() {
		t.Fatal(form, err)
	}
	_ = target.Set("ID", int64(2))
	if _, err := form.Save(true); err == nil {
		t.Fatal("aliased resolved record changed authorized identity")
	}
}

func TestModelMultipleChoiceDeduplicatesInputButRequiresEveryChoice(t *testing.T) {
	field := NewField("tags", ModelMultipleChoice)
	field.Resolve = func(_ context.Context, ids []string) ([]any, error) {
		if len(ids) != 2 || ids[0] != "one" || ids[1] != "two" {
			t.Fatal(ids)
		}
		return []any{int64(1)}, nil
	}
	if _, err := field.clean(context.Background(), []string{"one", "one", "two"}); err == nil {
		t.Fatal("silently dropped an unresolved requested choice")
	}
	field.Resolve = func(_ context.Context, ids []string) ([]any, error) { return []any{int64(2), int64(1)}, nil }
	if _, err := field.clean(context.Background(), []string{"one", "one", "two"}); err != nil {
		t.Fatal(err)
	}
}

func TestModelFormInitialOverridesMergeInstanceValues(t *testing.T) {
	record, _ := models.Bind(&modelSample{Name: "Instance", Role: "member"})
	form, err := NewModelForm(context.Background(), record, ModelFormOptions{Fields: []string{"Name", "Role"}}, WithInitial(map[string]any{"Name": "Override"}))
	if err != nil {
		t.Fatal(err)
	}
	name, _ := form.BoundField("Name")
	role, _ := form.BoundField("Role")
	if name.Value != "Override" || role.Value != "member" {
		t.Fatal(name.Value, role.Value)
	}
}
