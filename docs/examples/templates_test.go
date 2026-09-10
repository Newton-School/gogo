package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/templates"
)

func Example_template() {
	engine := templates.New(templates.Config{
		Loaders: []templates.Loader{templates.MapLoader{
			"product.html": `<h1>{{ name|upper }}</h1><p>{{ description }}</p>`,
		}},
	})
	output, err := engine.Render(context.Background(), "product.html", templates.Context{
		"name": "notebook", "description": "<b>Plain text</b>",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(output)
	// Output:
	// <h1>NOTEBOOK</h1><p>&lt;b&gt;Plain text&lt;/b&gt;</p>
}
