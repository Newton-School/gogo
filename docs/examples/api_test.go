package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/api"
)

func Example_serializer() {
	// docs:begin serializer-fields
	serializer, err := api.New(api.Definition{Fields: []api.Field{
		api.StringField("name"), api.DecimalField("price", 12, 2),
	}})
	if err != nil {
		panic(err)
	}
	// docs:end serializer-fields
	// docs:begin serializer-validate
	values, err := serializer.Validate(context.Background(), api.Values{
		"name": "Notebook", "price": "19.95",
	}, api.BindOptions{})
	if err != nil {
		panic(err)
	}
	// docs:end serializer-validate
	// docs:begin serializer-output
	representation, err := serializer.Representation(context.Background(), values)
	if err != nil {
		panic(err)
	}
	fmt.Println(representation["name"], representation["price"])
	// docs:end serializer-output
	// Output:
	// Notebook 19.95
}
