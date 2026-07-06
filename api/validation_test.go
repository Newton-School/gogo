package api

import (
	"context"
	"errors"
	"reflect"
	"testing"

	modelvalidation "github.com/Newton-School/gogo/models/validation"
)

func TestAPIValidationSupportsFieldObjectUniqueTogetherAndModelReuse(t *testing.T) {
	serializer := NewSerializer(
		StringField("title", FieldOptions{
			Required: true,
			Validators: []FieldValidator{
				func(value any) error {
					if len(value.(string)) < 4 {
						return errors.New("too short")
					}
					return nil
				},
			},
		}),
		SlugField("slug", FieldOptions{
			Required: true,
			Validators: []FieldValidator{
				UniqueValidator(func(value any) (bool, error) {
					return value != "taken", nil
				}),
			},
		}),
		StringField("locale", FieldOptions{Required: true}),
	).WithObjectValidators(
		ObjectValidator(func(map[string]any) ValidationErrors {
			return ValidationErrors{"state": {"invalid transition"}}
		}),
		UniqueTogetherValidator([]string{"slug", "locale"}, func(map[string]any) (bool, error) {
			return false, nil
		}),
		ModelValidationValidator(context.Background(),
			modelvalidation.ModelLevel("model", func(context.Context, map[string]any) error {
				return errors.New("model invalid")
			}),
		),
	)

	_, fieldErrors, ok := serializer.Validate(map[string]any{
		"title":  "Go",
		"slug":   "taken",
		"locale": "en",
	})
	if ok {
		t.Fatalf("Validate() ok = true, want errors")
	}

	want := map[string][]string{
		"__all__": {"fields must make a unique set", "model invalid"},
		"slug":    {"This field must be unique."},
		"state":   {"invalid transition"},
		"title":   {"too short"},
	}
	if !reflect.DeepEqual(fieldErrors, want) {
		t.Fatalf("fieldErrors = %#v, want %#v", fieldErrors, want)
	}

	ordered := OrderedErrorKeys(fieldErrors)
	wantOrder := []string{"__all__", "slug", "state", "title"}
	if !reflect.DeepEqual(ordered, wantOrder) {
		t.Fatalf("OrderedErrorKeys() = %#v, want %#v", ordered, wantOrder)
	}
}
