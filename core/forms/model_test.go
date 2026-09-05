package forms

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/models"
	"net/url"
	"testing"
)

type modelSample struct {
	models.Base
	ID   int64
	Name string
	Role string
}

func (*modelSample) Schema() models.Schema {
	return models.Schema{AppLabel: "test", Name: "Sample", Fields: []models.Field{models.BigAutoField("ID"), models.CharField("Name", models.WithMaxLength(100)), models.CharField("Role", models.WithMaxLength(100))}}
}

type formStore struct {
	events []string
	fail   bool
}

func (s *formStore) Atomic(ctx context.Context, fn func(context.Context) error) error {
	s.events = append(s.events, "begin")
	err := fn(ctx)
	if err != nil {
		s.events = append(s.events, "rollback")
	} else {
		s.events = append(s.events, "commit")
	}
	return err
}
func (s *formStore) Save(_ context.Context, r models.Record) error {
	s.events = append(s.events, "parent")
	return nil
}
func (s *formStore) SaveRelations(_ context.Context, _ models.Record, _ map[string][]any) error {
	s.events = append(s.events, "relations")
	if s.fail {
		return errors.New("failure")
	}
	return nil
}
func TestModelFormReadonlyExcludedAndAtomic(t *testing.T) {
	instance := &modelSample{Name: "Before", Role: "member"}
	record, err := models.Bind(instance)
	if err != nil {
		t.Fatal(err)
	}
	store := &formStore{}
	f, err := NewModelForm(context.Background(), record, ModelFormOptions{Fields: []string{"Name", "Role"}, Readonly: []string{"Role"}, Persistence: store}, WithData(url.Values{"Name": {"After"}, "Role": {"admin"}, "ID": {"99"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsValid() {
		t.Fatal(f.Errors())
	}
	if _, err = f.Save(false); err != nil || len(store.events) != 0 {
		t.Fatal("commit false wrote", err, store.events)
	}
	if instance.Role != "member" || instance.ID != 0 {
		t.Fatal("readonly injection")
	}
	if _, err = f.Save(true); err != nil {
		t.Fatal(err)
	}
	want := []string{"begin", "parent", "relations", "commit"}
	for i, v := range want {
		if store.events[i] != v {
			t.Fatal(store.events)
		}
	}
}
func TestModelFormRelationFailureRollsBackBoundary(t *testing.T) {
	record, _ := models.Bind(&modelSample{Name: "Before", Role: "member"})
	store := &formStore{fail: true}
	f, _ := NewModelForm(context.Background(), record, ModelFormOptions{Fields: []string{"Name"}, Persistence: store}, WithData(url.Values{"Name": {"After"}}))
	if _, err := f.Save(true); err == nil {
		t.Fatal("failure lost")
	}
	if store.events[len(store.events)-1] != "rollback" {
		t.Fatal(store.events)
	}
}
func TestEmptyExtraFormIgnored(t *testing.T) {
	set, err := BindFormSet(context.Background(), []Field{NewField("name", Char)}, url.Values{"form-TOTAL_FORMS": {"1"}, "form-INITIAL_FORMS": {"0"}, "form-0-name": {""}}, FormSetOptions{})
	if err != nil || !set.IsValid() || len(set.Ordered) != 0 {
		t.Fatal(set, err)
	}
}
