package api_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Newton-School/gogo/core/api"
)

func ExampleSerializer_OutputSchema() {
	display := api.ComputedField("display", func(context.Context, api.ValueReader) (any, error) {
		panic("schema generation must not execute application code")
	})
	display.OutputSchema = &api.WireSchema{Type: "string"}
	secret := api.StringField("secret")
	secret.WriteOnly = true
	serializer, err := api.New(api.Definition{Fields: []api.Field{
		api.IntegerField("id"), display, secret,
	}})
	if err != nil {
		panic(err)
	}
	// Use Redactable when a surrounding resource can remove output fields.
	document, err := serializer.OutputSchema(context.Background(), api.OutputSchemaOptions{Redactable: true})
	if err != nil {
		panic(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(document, &schema); err != nil {
		panic(err)
	}
	properties := schema["properties"].(map[string]any)
	fmt.Println(schema["$schema"])
	fmt.Println(len(properties), properties["secret"] == nil)
	_, promisesRequiredFields := schema["required"]
	fmt.Println(promisesRequiredFields)
	// Output:
	// https://json-schema.org/draft/2020-12/schema
	// 2 true
	// false
}
