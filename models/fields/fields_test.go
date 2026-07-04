package fields

import (
	"errors"
	"testing"
)

func TestBaseFieldAppliesOptionDefaults(t *testing.T) {
	field := NewBaseField("text", Options{Name: "title"}, map[string]string{"postgres": "text"})

	if field.Name() != "title" {
		t.Fatalf("Name() = %q, want title", field.Name())
	}
	if field.ColumnName() != "title" {
		t.Fatalf("ColumnName() = %q, want title", field.ColumnName())
	}
	if !field.IsEditable() {
		t.Fatalf("IsEditable() = false, want true")
	}
	if !field.IsSerializable() {
		t.Fatalf("IsSerializable() = false, want true")
	}
	if got := field.ColumnType("postgres"); got != "text" {
		t.Fatalf("ColumnType(postgres) = %q, want text", got)
	}
}

func TestBaseFieldUsesExplicitColumnName(t *testing.T) {
	field := NewBaseField("integer", Options{Name: "userID", Column: "user_id"}, map[string]string{"default": "integer"})

	if field.ColumnName() != "user_id" {
		t.Fatalf("ColumnName() = %q, want user_id", field.ColumnName())
	}
}

func TestBaseFieldValidationRejectsNullBlankAndValidatorErrors(t *testing.T) {
	required := NewBaseField("text", Options{Name: "title"}, nil)
	if err := required.Validate(nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("Validate(nil) error = %v, want ErrValidation", err)
	}
	if err := required.Validate(""); !errors.Is(err, ErrValidation) {
		t.Fatalf("Validate(blank) error = %v, want ErrValidation", err)
	}

	nullable := NewBaseField("text", Options{Name: "title", Null: true, Blank: true}, nil)
	if err := nullable.Validate(nil); err != nil {
		t.Fatalf("nullable Validate(nil) error = %v", err)
	}
	if err := nullable.Validate(""); err != nil {
		t.Fatalf("blank Validate(\"\") error = %v", err)
	}

	invalid := NewBaseField("text", Options{
		Name: "title",
		Validators: []Validator{
			func(any) error { return errors.New("not valid") },
		},
	}, nil)
	if err := invalid.Validate("value"); !errors.Is(err, ErrValidation) {
		t.Fatalf("validator error = %v, want ErrValidation", err)
	}
}

func TestBaseFieldCloneIsIndependent(t *testing.T) {
	editable := false
	field := NewBaseField("text", Options{
		Name:          "title",
		Editable:      &editable,
		ErrorMessages: map[string]string{"required": "Required"},
	}, nil)

	clone := field.Clone().(*BaseField)
	clone.options.Name = "changed"
	clone.options.ErrorMessages["required"] = "Changed"

	if field.Name() != "title" {
		t.Fatalf("original Name() = %q, want title", field.Name())
	}
	if field.Options().ErrorMessages["required"] != "Required" {
		t.Fatalf("original error message changed: %#v", field.Options().ErrorMessages)
	}
	if field.IsEditable() {
		t.Fatalf("IsEditable() = true, want explicit false")
	}
}

func TestCustomFieldColumnTypesMetadataAndConversion(t *testing.T) {
	field := NewCustomField(Options{Name: "embedding", Column: "embedding_col", Null: true, DBIndex: true}, CustomConfig{
		Kind:        "vector",
		ColumnTypes: map[string]string{"postgres": "vector(384)", "sqlite": "text"},
		ValidateFunc: func(value any) error {
			if value == "bad" {
				return errors.New("bad vector")
			}
			return nil
		},
		ToDBFunc: func(value any) (any, error) {
			return "db:" + value.(string), nil
		},
		FromDBFunc: func(value any) (any, error) {
			return "model:" + value.(string), nil
		},
	})

	if got := field.ColumnType("postgres"); got != "vector(384)" {
		t.Fatalf("postgres column type = %q, want vector(384)", got)
	}
	if got := field.ColumnType("sqlite"); got != "text" {
		t.Fatalf("sqlite column type = %q, want text", got)
	}
	if err := field.Validate("ok"); err != nil {
		t.Fatalf("Validate(ok) error = %v", err)
	}
	if err := field.Validate("bad"); !errors.Is(err, ErrValidation) {
		t.Fatalf("Validate(bad) error = %v, want ErrValidation", err)
	}
	dbValue, err := field.ToDB("value")
	if err != nil || dbValue != "db:value" {
		t.Fatalf("ToDB() = %#v, %v", dbValue, err)
	}
	modelValue, err := field.FromDB("value")
	if err != nil || modelValue != "model:value" {
		t.Fatalf("FromDB() = %#v, %v", modelValue, err)
	}

	meta := Metadata(field, "postgres")
	if meta.Name != "embedding" || meta.Column != "embedding_col" || meta.Kind != "vector(384)" || !meta.Null || !meta.DBIndex {
		t.Fatalf("metadata = %#v", meta)
	}

	clone := field.Clone().(*CustomField)
	clone.config.ColumnTypes["postgres"] = "vector(768)"
	if got := field.ColumnType("postgres"); got != "vector(384)" {
		t.Fatalf("original column type changed to %q", got)
	}
}
