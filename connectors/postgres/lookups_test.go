package postgres_test

import (
	"context"
	"github.com/Newton-School/gogo/core/orm"
	"testing"
)

func TestTextLookupsEscapeLiteralWildcards(t *testing.T) {
	_, store := setupProducts(t)
	ctx := context.Background()
	for _, name := range []string{`prefix 50%_\ Tail`, "prefix 500x Tail"} {
		if err := store.Save(ctx, &product{Name: name, Amount: "1.00"}, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		lookup string
		value  string
		count  int64
	}{{"contains", `50%_\`, 1}, {"icontains", `50%_\ tAIL`, 1}, {"startswith", "prefix 50%", 1}, {"istartswith", "PREFIX 50%", 1}, {"endswith", `_\ Tail`, 1}, {"iendswith", `_\ tAIL`, 1}} {
		count, err := orm.For(store, func() *product { return &product{} }).Filter(orm.Q("name__"+test.lookup, test.value)).Count(ctx)
		if err != nil || count != test.count {
			t.Fatal(test.lookup, count, err)
		}
	}
}
