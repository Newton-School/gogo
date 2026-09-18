package examples_test

import (
	"fmt"
	"net/url"

	"github.com/Newton-School/gogo/core/pagination"
)

func Example_pagination() {
	pager, err := pagination.New(pagination.Config{
		Mode: pagination.PageNumber, DefaultSize: 10, MaxSize: 50,
	})
	if err != nil {
		panic(err)
	}
	page, err := pager.Parse(url.Values{"page": {"2"}})
	if err != nil {
		panic(err)
	}
	// Apply scope first, then Offset(page.Offset) and Limit(page.Size) to the query.
	fmt.Println("offset:", page.Offset, "size:", page.Size)
	// Output:
	// offset: 10 size: 10
}
