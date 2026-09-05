package api

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func querySchema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Product", Fields: []models.Field{models.BigAutoField("id"), models.CharField("name", models.WithMaxLength(100)), models.BigIntegerField("price"), models.BooleanField("active"), models.JSONField("data", models.Nullable)}}
}
func TestFilterBackendBoundsTypedInputsAndDeterministicOrdering(t *testing.T) {
	config := FilterConfig{Filters: []Filter{{"price", "price", "gte"}, {"ids", "id", "in"}, {"span", "price", "range"}, {"missing", "name", "isnull"}}, SearchFields: []string{"name"}, OrderingFields: []string{"price"}, DefaultOrdering: []string{"name"}}
	f, err := NewFilterBackend(querySchema(), config)
	if err != nil {
		t.Fatal(err)
	}
	config.Filters[0].Field = "unknown"
	config.SearchFields[0] = "data"
	config.OrderingFields[0] = "data"
	config.DefaultOrdering[0] = "data"
	options, err := f.Parse(context.Background(), url.Values{"price": {"12"}, "ids": {"1", "2"}, "span": {"1", "20"}, "search": {"red blue"}, "ordering": {"-price"}})
	if err != nil || !reflect.DeepEqual(options.Ordering, []string{"-price", "id"}) {
		t.Fatal(options.Ordering, err)
	}
	defaults, err := f.Parse(context.Background(), url.Values{})
	if err != nil || !reflect.DeepEqual(defaults.Ordering, []string{"name", "id"}) {
		t.Fatal(defaults.Ordering, err)
	}
	for _, values := range []url.Values{{"tenant": {"forged"}}, {"price": {"12", "13"}}, {"price": {"not numeric"}}, {"ids": {}}, {"span": {"1"}}, {"span": {"1", "2", "3"}}, {"ordering": {"price,price"}}, {"ordering": {"name"}}, {"ordering": {"price DESC; DROP TABLE x"}}, {"search": {"a", "b"}}, {"missing": {"1"}}, {"unknown": {"x"}}, {"search": {"\xff"}}} {
		if _, err := f.Parse(context.Background(), values); err == nil {
			t.Fatal("invalid query accepted", values)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Parse(ctx, nil); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestInvalidFilterDeclarations(t *testing.T) {
	for _, config := range []FilterConfig{{Filters: []Filter{{"page", "id", "exact"}}}, {Filters: []Filter{{"x", "data", "exact"}}}, {Filters: []Filter{{"x", "active", "gt"}}}, {Filters: []Filter{{"x", "price", "contains"}}}, {Filters: []Filter{{"x", "price", "raw"}}}, {Filters: []Filter{{"x", "price", ""}, {"x", "id", ""}}}, {SearchFields: []string{"price"}}, {DefaultOrdering: []string{"unknown"}}, {OrderingFields: []string{"name", "name"}}} {
		if _, err := NewFilterBackend(querySchema(), config); err == nil {
			t.Fatal("invalid declaration accepted", config)
		}
	}
}

func TestQueryOperandsDoNotRunWholeModelValidation(t *testing.T) {
	calls := 0
	schema := models.Schema{AppLabel: "shop", Name: "Contact", Fields: []models.Field{models.BigAutoField("id"), models.EmailField("email"), models.URLField("url"), models.CharField("code", models.WithMinLength(5), models.WithValidators(func(context.Context, any) error {
		calls++
		return models.Invalid("write_only", "not a query validator")
	})), models.IntegerField("score", models.WithBounds(5, 10), models.WithValidators(func(context.Context, any) error {
		calls++
		return models.Invalid("write_only", "not a query validator")
	}))}}
	f, err := NewFilterBackend(schema, FilterConfig{Filters: []Filter{{"email", "email", "icontains"}, {"url", "url", "startswith"}, {"code", "code", "contains"}, {"score", "score", "lt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Parse(context.Background(), url.Values{"email": {"@example.com"}, "url": {"https://"}, "code": {"a"}, "score": {"1"}}); err != nil || calls != 0 {
		t.Fatal("whole model validation ran on query operands", err, calls)
	}
	if _, err := cleanQueryValue(context.Background(), schema.Fields[1], "not a full email"); err != nil {
		t.Fatal("text equality required full model validation", err)
	}
	if _, err := cleanQueryValue(context.Background(), schema.Fields[4], "not a number"); err == nil {
		t.Fatal("lost intrinsic numeric type validation")
	}
}
