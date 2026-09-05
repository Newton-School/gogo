package models_test

import (
	"github.com/Newton-School/gogo/core/models"
	"testing"
)

func TestAutomaticIntermediaryProvenanceAndMapBinding(t *testing.T) {
	target := models.Schema{AppLabel: "test", Name: "Tag", Fields: []models.Field{models.BigAutoField("id")}}
	field := models.ManyToManyField("tags", models.Relation{Target: target.Key()})
	source := models.Schema{AppLabel: "test", Name: "Post", Fields: []models.Field{models.BigAutoField("id"), field}}
	through, err := models.ImplicitThrough(source, field, target)
	if err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(through); err == nil {
		t.Fatal("caller-controlled automatic provenance accepted")
	}
	for _, schema := range []models.Schema{source, target} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal("freeze not idempotent", err)
	}
	actual, ok := registry.Get(through.Key())
	if !ok || !registry.IsAutomatic(actual.Key()) || registry.IsAutomatic(source.Key()) || actual.AutoCreatedField != "tags" {
		t.Fatal(actual, ok)
	}
	for _, name := range []string{"source_id", "target_id"} {
		field, _ := actual.Field(name)
		if len(field.Relation.TargetFields) != 1 || field.Relation.TargetFields[0] != "id" {
			t.Fatal(field)
		}
	}
	record, err := models.NewRecord(actual)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := models.Bind(record)
	if err != nil || bound != record {
		t.Fatal(bound, err)
	}
	if value, err := bound.Get("source_id"); err != nil || value != nil {
		t.Fatal(value, err)
	}
	record.State().Deferred = map[string]bool{"source_id": true}
	if _, err := record.Get("source_id"); err == nil {
		t.Fatal("deferred map field readable")
	}
	if err := record.Set("source_id", int64(1)); err != nil {
		t.Fatal(err)
	}
	if record.State().Deferred["source_id"] {
		t.Fatal("loaded map field remained deferred")
	}
}

func TestServerTimestampFieldsAreNotEditable(t *testing.T) {
	for _, option := range []models.FieldOption{func(f *models.Field) { f.AutoNow = true }, func(f *models.Field) { f.AutoNowAdd = true }} {
		if models.DateTimeField("timestamp", option).IsEditable() {
			t.Fatal("server-managed timestamp is form-editable")
		}
	}
	if !models.DateTimeField("timestamp").IsEditable() {
		t.Fatal("ordinary timestamp is not form-editable")
	}
}
