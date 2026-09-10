package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/api"
)

func Example_serializer() {
	serializer, err := api.New(api.Definition{Fields: []api.Field{
		api.StringField("name"), api.DecimalField("price", 12, 2),
	}})
	if err != nil {
		panic(err)
	}
	values, err := serializer.Validate(context.Background(), api.Values{
		"name": "Notebook", "price": "19.95",
	}, api.BindOptions{})
	if err != nil {
		panic(err)
	}
	representation, err := serializer.Representation(context.Background(), values)
	if err != nil {
		panic(err)
	}
	fmt.Println(representation["name"], representation["price"])
	// Output:
	// Notebook 19.95
}
