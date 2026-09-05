package models_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/models"
	"testing"
)

type person struct {
	models.Base
	ID         int64
	Name       string
	Age        int32
	Email      string
	cleanCalls int
}

func (p *person) Schema() models.Schema {
	return models.Schema{AppLabel: "test", Name: "Person", Fields: []models.Field{models.BigAutoField("ID"), models.CharField("Name", models.WithMaxLength(3)), models.IntegerField("Age"), models.EmailField("Email", models.UniqueValue)}}
}
func (p *person) Clean(context.Context) error { p.cleanCalls++; return nil }

type checks struct {
	unique, constraints bool
	exclude             []string
}

func (c *checks) ValidateUnique(_ context.Context, _ models.Record, exclude []string) error {
	c.unique = true
	c.exclude = append([]string(nil), exclude...)
	e := &models.ValidationError{}
	e.Add("Email", "unique", "Already used.")
	return e
}
func (c *checks) ValidateConstraints(_ context.Context, _ models.Record, exclude []string) error {
	c.constraints = true
	c.exclude = append([]string(nil), exclude...)
	return nil
}
func TestFullCleanAggregatesStages(t *testing.T) {
	p := &person{Name: "too long", Email: "a@example.com"}
	record, err := models.Bind(p)
	if err != nil {
		t.Fatal(err)
	}
	checker := &checks{}
	err = models.FullClean(context.Background(), record, models.CleanOptions{}, checker)
	var validation *models.ValidationError
	if !errors.As(err, &validation) || len(validation.Fields["Name"]) != 1 || len(validation.Fields["Email"]) != 1 {
		t.Fatalf("errors not aggregated: %#v %v", validation, err)
	}
	if p.cleanCalls != 1 || !checker.unique || !checker.constraints {
		t.Fatal("validation skipped stage")
	}
	if len(checker.exclude) != 2 {
		t.Fatalf("exclude=%v", checker.exclude)
	}
}
func TestFieldValidation(t *testing.T) {
	cases := []struct {
		field models.Field
		value any
		valid bool
	}{{models.PositiveIntegerField("N"), int64(-1), false}, {models.IntegerField("N"), int64(1) << 40, false}, {models.BooleanField("B"), false, true}, {models.CharField("S", models.WithMaxLength(1)), "é", true}, {models.EmailField("E"), "Alice <a@example.com>", false}, {models.DecimalField("D", 5, 2), "999.99", true}, {models.DecimalField("D", 5, 2), "9999.99", false}, {models.JSONField("J"), `{"x":false}`, true}, {models.UUIDField("U"), "not-a-uuid", false}}
	for _, c := range cases {
		if err := c.field.Validate(context.Background(), c.value); (err == nil) != c.valid {
			t.Errorf("%s %v: %v", c.field.Kind, c.value, err)
		}
	}
}
func TestRecordDoesNotSilentlyOverflow(t *testing.T) {
	p := &person{}
	r, err := models.Bind(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Set("Age", int64(1)<<40); err == nil {
		t.Fatal("overflow accepted")
	}
	if err = r.Set("ID", int64(8)); err != nil {
		t.Fatal(err)
	}
	if p.ID != 8 || !p.ModelState().Adding() {
		t.Fatal("unexpected instance state")
	}
}
func TestRegistryFreezesAndCopies(t *testing.T) {
	schema := (&person{}).Schema()
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	schema.Fields[1].Name = "mutated"
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(schema); err == nil {
		t.Fatal("registered after freeze")
	}
	s, _ := registry.Get("test.Person")
	if s.Fields[1].Name != "Name" {
		t.Fatal("registry mutated")
	}
}
