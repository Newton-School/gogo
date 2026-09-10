package examples_test

import (
	"context"
	"fmt"
	"net/url"

	"github.com/Newton-School/gogo/core/forms"
)

func Example_form() {
	name := forms.NewField("name", forms.Char)
	name.MaxLength = 120
	price := forms.NewField("price", forms.Decimal)
	price.MaxDigits, price.DecimalPlaces = 12, 2
	form, err := forms.New([]forms.Field{name, price},
		forms.WithContext(context.Background()),
		forms.WithData(url.Values{"name": {"  Notebook  "}, "price": {"19.95"}}),
	)
	if err != nil {
		panic(err)
	}
	if !form.IsValid() {
		panic("example input failed validation")
	}
	fmt.Println(form.CleanedData()["name"])
	fmt.Println(form.CleanedData()["price"])
	// Output:
	// Notebook
	// 19.95
}
