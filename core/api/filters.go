package api

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
)

// Filter maps one public query parameter to one explicitly allowed model
// field/operator. IN/range use repeated parameters, not ambiguous CSV values.
type Filter struct{ Parameter, Field, Lookup string }
type FilterConfig struct {
	Filters                                       []Filter
	SearchFields, OrderingFields, DefaultOrdering []string
}
type FilterBackend struct {
	schema                     models.Schema
	filters                    map[string]Filter
	search, ordering, defaults []string
}
type QueryOptions struct {
	Predicate db.Predicate
	Ordering  []string
}

func NewFilterBackend(schema models.Schema, config FilterConfig) (*FilterBackend, error) {
	if err := schema.Validate(); err != nil {
		return nil, err
	}
	f := &FilterBackend{schema: schema.Clone(), filters: map[string]Filter{}, search: slices.Clone(config.SearchFields), ordering: slices.Clone(config.OrderingFields), defaults: slices.Clone(config.DefaultOrdering)}
	for _, filter := range config.Filters {
		if filter.Lookup == "" {
			filter.Lookup = "exact"
		}
		field, ok := schema.Field(filter.Field)
		_, duplicate := f.filters[filter.Parameter]
		if !validFilterParameter(filter.Parameter) || duplicate || !ok || !queryScalar(field) || !allowedLookup(field, filter.Lookup) {
			return nil, errors.New("api: invalid filter declaration")
		}
		f.filters[filter.Parameter] = filter
	}
	for _, group := range [][]string{f.search, f.ordering} {
		seen := map[string]bool{}
		for _, name := range group {
			field, ok := schema.Field(name)
			if !ok || !queryScalar(field) || seen[name] {
				return nil, errors.New("api: invalid query field declaration")
			}
			seen[name] = true
		}
	}
	for _, name := range f.search {
		field, _ := schema.Field(name)
		if !queryText(field) {
			return nil, errors.New("api: search requires text fields")
		}
	}
	if _, err := f.totalOrder(f.defaults, false); err != nil {
		return nil, err
	}
	return f, nil
}

func validFilterParameter(name string) bool {
	if len(name) > 128 || !models.ValidIdentifier(name) {
		return false
	}
	switch name {
	case "page", "page_size", "limit", "offset", "cursor", "search", "ordering":
		return false
	}
	return true
}
func queryText(field models.Field) bool {
	switch field.Kind {
	case models.Char, models.Text, models.Slug, models.Email, models.URL:
		return true
	}
	return false
}
func queryScalar(field models.Field) bool {
	if !field.IsStored() || field.Relation != nil {
		return false
	}
	switch field.Kind {
	case models.Char, models.Text, models.Slug, models.Email, models.URL, models.UUID, models.GenericIPAddress, models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto, models.Float, models.Decimal, models.Boolean, models.Date, models.DateTime, models.Time, models.Duration:
		return true
	}
	return false
}
func allowedLookup(field models.Field, lookup string) bool {
	switch lookup {
	case "exact", "in", "isnull":
		return true
	case "gt", "gte", "lt", "lte", "range":
		return field.Kind != models.Boolean
	case "iexact", "contains", "icontains", "startswith", "istartswith", "endswith", "iendswith":
		return queryText(field)
	}
	return false
}
func invalidQuery() error {
	return mediaError(400, "INVALID_QUERY", "Invalid collection query parameters")
}

// Parse rejects undeclared parameters and expressions. It never receives or
// replaces the mandatory resource scope; its predicate is an additional AND.
func (f *FilterBackend) Parse(ctx context.Context, values url.Values) (QueryOptions, error) {
	if f == nil {
		return QueryOptions{}, errors.New("api: filter backend required")
	}
	if err := ctx.Err(); err != nil {
		return QueryOptions{}, err
	}
	if len(values) > 64 {
		return QueryOptions{}, invalidQuery()
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var predicates []db.Predicate
	ordering := slices.Clone(f.defaults)
	orderingRequested := false
	for _, key := range keys {
		items := values[key]
		if len(items) < 1 || len(items) > 100 {
			return QueryOptions{}, invalidQuery()
		}
		for _, item := range items {
			maximum := 4096
			if key == "cursor" {
				maximum = pagination.MaxCursorBytes
			}
			if !utf8.ValidString(item) || len(item) > maximum || strings.ContainsRune(item, 0) {
				return QueryOptions{}, invalidQuery()
			}
		}
		switch key {
		case "page", "page_size", "limit", "offset", "cursor":
			continue
		case "ordering":
			orderingRequested = true
			if len(items) != 1 || items[0] == "" {
				return QueryOptions{}, invalidQuery()
			}
			ordering = strings.Split(items[0], ",")
			if len(ordering) > 16 {
				return QueryOptions{}, invalidQuery()
			}
		case "search":
			if len(items) != 1 || len(f.search) == 0 || len(items[0]) > 256 {
				return QueryOptions{}, invalidQuery()
			}
			terms := strings.Fields(items[0])
			if len(terms) > 16 {
				return QueryOptions{}, invalidQuery()
			}
			for _, term := range terms {
				matches := make([]db.Predicate, 0, len(f.search))
				for _, field := range f.search {
					matches = append(matches, orm.Q(field+"__icontains", term))
				}
				predicates = append(predicates, orm.Or(matches...))
			}
		default:
			filter, ok := f.filters[key]
			if !ok {
				return QueryOptions{}, invalidQuery()
			}
			field, _ := f.schema.Field(filter.Field)
			if filter.Lookup != "in" && filter.Lookup != "range" && len(items) != 1 || filter.Lookup == "range" && len(items) != 2 {
				return QueryOptions{}, invalidQuery()
			}
			cleaned := make([]any, 0, len(items))
			for _, item := range items {
				var value any
				var err error
				if filter.Lookup == "isnull" {
					if item == "true" {
						value = true
					} else if item == "false" {
						value = false
					} else {
						return QueryOptions{}, invalidQuery()
					}
				} else {
					value, err = cleanQueryValue(ctx, field, item)
					if err != nil {
						if ctx.Err() != nil {
							return QueryOptions{}, ctx.Err()
						}
						return QueryOptions{}, invalidQuery()
					}
				}
				cleaned = append(cleaned, value)
			}
			var value any = cleaned[0]
			if filter.Lookup == "in" || filter.Lookup == "range" {
				value = cleaned
			}
			predicates = append(predicates, orm.Q(filter.Field+"__"+filter.Lookup, value))
		}
	}
	order, err := f.totalOrder(ordering, orderingRequested)
	if err != nil {
		return QueryOptions{}, invalidQuery()
	}
	if err := ctx.Err(); err != nil {
		return QueryOptions{}, err
	}
	return QueryOptions{Predicate: orm.And(predicates...), Ordering: order}, nil
}

// Query operands are not values being saved. In particular a substring of an
// email/URL and text shorter than a model's minimum length are valid operands.
// Keep intrinsic type normalization, but never execute business validators or
// constrain comparisons to the model's choices/current write-time bounds.
func cleanQueryValue(ctx context.Context, field models.Field, raw any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if queryText(field) {
		value, ok := raw.(string)
		if !ok || !utf8.ValidString(value) || len(value) > 4096 || strings.ContainsRune(value, 0) {
			return nil, invalidQuery()
		}
		return value, nil
	}
	field.Validators = nil
	field.Choices = nil
	field.Min = nil
	field.Max = nil
	field.Blank = false
	return field.Clean(ctx, raw)
}

func (f *FilterBackend) totalOrder(order []string, public bool) ([]string, error) {
	result := slices.Clone(order)
	seen := map[string]bool{}
	for _, value := range order {
		name := strings.TrimPrefix(value, "-")
		field, ok := f.schema.Field(name)
		if !ok || !queryScalar(field) || seen[name] || public && !slices.Contains(f.ordering, name) {
			return nil, errors.New("api: invalid ordering")
		}
		seen[name] = true
	}
	for _, field := range f.schema.PKFields() {
		if !queryScalar(field) {
			return nil, errors.New("api: unsupported resource primary key")
		}
		if !seen[field.Name] {
			result = append(result, field.Name)
		}
	}
	return result, nil
}
