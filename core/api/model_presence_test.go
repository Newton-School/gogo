package api

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestModelNullablePresenceDoesNotInventDefaultsOrWeakenUniqueInput(t *testing.T) {
	for _, unique := range []bool{false, true} {
		field := models.CharField("notes", models.Nullable)
		field.Unique = unique
		schema := models.Schema{AppLabel: "shop", Name: "Item", Fields: []models.Field{models.BigAutoField("id"), field}}
		serializer, err := FromModel(schema, ModelOptions{Fields: []string{"notes"}})
		if err != nil {
			t.Fatal(err)
		}
		value, err := serializer.Validate(context.Background(), Values{}, BindOptions{})
		if unique {
			if err == nil {
				t.Fatal("unique input unexpectedly omitted")
			}
		} else if err != nil || len(value) != 0 {
			t.Fatal("nullable omission invented input", value, err)
		}
		value, err = serializer.Validate(context.Background(), Values{"notes": nil}, BindOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := value["notes"]; !ok || v != nil {
			t.Fatal("explicit null lost presence")
		}
	}
	plain, err := New(Definition{Fields: []Field{StringField("notes", models.Nullable)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Validate(context.Background(), Values{}, BindOptions{}); err == nil {
		t.Fatal("plain nullable scalar stopped requiring explicit input")
	}
}
