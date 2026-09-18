package examples_test

import (
	"context"
	"fmt"
	"net/url"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

func Example_modelTextOptions() {
	// docs:begin model-text-options
	name := models.CharField("name",
		models.WithStructField("Name"), models.WithColumn("display_name"),
		models.WithMinLength(2), models.WithMaxLength(120),
		models.WithLabel("Product name"), models.WithHelpText("Shown in the catalog."),
	)
	// docs:end model-text-options
	// docs:begin model-clean
	value, err := name.Clean(context.Background(), "Notebook")
	if err != nil {
		panic(err)
	}
	fmt.Println(value) // Notebook; nothing has been saved.
	// docs:end model-clean
	fmt.Println(name.GoField(), name.DBColumn())
	// Output:
	// Notebook
	// Name display_name
}

func Example_modelNullAndBlank() {
	// docs:begin model-null-blank
	note := models.TextField("note", models.Nullable, models.Optional)
	_, nullErr := note.Clean(context.Background(), nil)
	_, blankErr := note.Clean(context.Background(), "")
	fmt.Println(nullErr == nil, blankErr == nil) // true true
	// docs:end model-null-blank
	// Output: true true
}

func Example_modelChoices() {
	// docs:begin model-choices
	status := models.CharField("status", models.WithMaxLength(12),
		models.WithChoices(
			models.Choice{Value: "draft", Label: "Draft"},
			models.Choice{Value: "published", Label: "Published"},
		),
		models.WithDefault("draft"),
	)
	_, err := status.Clean(context.Background(), "unknown")
	fmt.Println(err != nil) // true: not an allowed choice.
	// docs:end model-choices
	// Output: true
}

func Example_modelBounds() {
	// docs:begin model-bounds
	stock := models.IntegerField("stock", models.WithBounds(0, 1000))
	_, err := stock.Clean(context.Background(), -1)
	fmt.Println(err != nil) // true: below the minimum.
	// docs:end model-bounds
	// Output: true
}

func Example_modelValidator() {
	// docs:begin model-validator
	name := models.CharField("name", models.WithValidators(
		func(ctx context.Context, value any) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if value == "reserved" {
				return models.Invalid("reserved", "Choose another name.")
			}
			return nil
		},
	))
	_, err := name.Clean(context.Background(), "reserved")
	fmt.Println(err != nil) // true
	// docs:end model-validator
	// Output: true
}

func Example_formOptions() {
	// docs:begin form-options
	name := forms.NewField("name", forms.Char)
	name.Required = false // NewField defaults to true.
	name.Label = "Product name"
	name.HelpText = "Use the public catalog name."
	name.MinLength, name.MaxLength = 2, 120
	name.Widget = forms.InputWidget{Type: "text"}
	// docs:end form-options
	form, err := forms.New([]forms.Field{name}, forms.WithData(url.Values{"name": {"Notebook"}}))
	if err != nil {
		panic(err)
	}
	fmt.Println(form.IsValid(), form.CleanedData()["name"])
	// Output: true Notebook
}

func Example_formErrors() {
	// docs:begin form-errors
	quantity := forms.NewField("quantity", forms.Integer)
	form, err := forms.New([]forms.Field{quantity},
		forms.WithData(url.Values{"quantity": {"not a number"}}))
	if err != nil {
		panic(err)
	}
	fmt.Println(form.IsValid())         // false
	fmt.Println(len(form.Errors()) > 0) // true
	// docs:end form-errors
	// Output:
	// false
	// true
}

func Example_serializerDirections() {
	// docs:begin serializer-directions
	id := api.IntegerField("id")
	id.ReadOnly = true // Input cannot replace this value.
	note := api.StringField("note")
	note.WriteOnly = true // Output never includes this value.
	serializer, err := api.New(api.Definition{Fields: []api.Field{id, note}})
	if err != nil {
		panic(err)
	}
	// docs:end serializer-directions
	values, err := serializer.Validate(context.Background(), api.Values{"id": 99, "note": "Private"}, api.BindOptions{})
	if err != nil {
		panic(err)
	}
	_, inputID := values["id"]
	output, err := serializer.Representation(context.Background(), api.Values{"id": int64(42), "note": "Private"})
	if err != nil {
		panic(err)
	}
	_, outputNote := output["note"]
	fmt.Println(inputID, outputNote)
	// Output: false false
}

func Example_serializerPartial() {
	serializer, err := api.New(api.Definition{Fields: []api.Field{api.StringField("name"), api.DecimalField("price", 12, 2)}})
	if err != nil {
		panic(err)
	}
	// docs:begin serializer-partial
	values, err := serializer.Validate(context.Background(),
		api.Values{"name": "Revised name"}, api.BindOptions{Partial: true})
	if err != nil {
		panic(err)
	}
	_, hasPrice := values["price"]
	fmt.Println(values["name"], hasPrice) // Revised name false
	// docs:end serializer-partial
	// Output: Revised name false
}
