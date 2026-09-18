package examples_test

import (
	"fmt"
	"net/url"

	"github.com/Newton-School/gogo/core/pagination"
)

func Example_pagination() {
	// docs:begin pagination-config
	pager, err := pagination.New(pagination.Config{
		Mode: pagination.PageNumber, DefaultSize: 10, MaxSize: 50,
	})
	if err != nil {
		panic(err)
	}
	// docs:end pagination-config
	// docs:begin pagination-parse
	page, err := pager.Parse(url.Values{"page": {"2"}})
	if err != nil {
		panic(err)
	}
	// Apply scope first, then Offset(page.Offset) and Limit(page.Size) to the query.
	fmt.Println("offset:", page.Offset, "size:", page.Size)
	// docs:end pagination-parse
	// Output:
	// offset: 10 size: 10
}
