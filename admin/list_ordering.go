package admin

import (
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/models"
)

func validateListOrdering(options ModelAdmin) error {
	for _, column := range options.Columns {
		if column.Ordering == "" {
			continue
		}
		name := strings.TrimPrefix(column.Ordering, "-")
		field, exists := options.Schema.Field(name)
		if !models.ValidIdentifier(column.Name) || !models.ValidIdentifier(name) || !exists || !sortableMappingField(field) {
			return errors.New("admin: display ordering requires one stored scalar model field")
		}
	}
	seen := map[string]bool{}
	for _, name := range options.SortableBy {
		if !models.ValidIdentifier(name) || seen[name] {
			return errors.New("admin: sortable columns must be unique displayed identifiers")
		}
		if _, ok := displayOrdering(options, name); !ok {
			return errors.New("admin: sortable column requires a displayed ordering target")
		}
		seen[name] = true
	}
	return nil
}

func sortableMappingField(field models.Field) bool {
	if !field.IsStored() || field.Relation != nil {
		return false
	}
	switch field.Kind {
	case models.SmallAuto, models.Auto, models.BigAuto, models.SmallInteger, models.Integer, models.BigInteger,
		models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.Float, models.Decimal,
		models.Boolean, models.Char, models.Text, models.Slug, models.Email, models.URL, models.UUID, models.GenericIPAddress,
		models.FilePath, models.File, models.Image, models.Date, models.DateTime, models.Time, models.Duration:
		return true
	default:
		return false
	}
}

// Resolve only registered display names. Stored-field columns retain their
// existing behavior; computed mappings never accept SQL or relation paths.
func displayOrdering(options ModelAdmin, name string) (string, bool) {
	if !slices.Contains(options.ListDisplay, name) {
		return "", false
	}
	for _, column := range options.Columns {
		if column.Name == name && column.Ordering != "" {
			return column.Ordering, true
		}
	}
	if field, ok := options.Schema.Field(name); ok && field.IsStored() {
		return name, true
	}
	return "", false
}

func userDisplayOrdering(options ModelAdmin, name string) (string, bool) {
	if options.SortableBy != nil && !slices.Contains(options.SortableBy, name) {
		return "", false
	}
	return displayOrdering(options, name)
}

func reverseOrdering(order string) string {
	if strings.HasPrefix(order, "-") {
		return strings.TrimPrefix(order, "-")
	}
	return "-" + order
}

func listOrdering(options ModelAdmin, values url.Values) ([]string, error) {
	if len(values["o"]) > 1 {
		return nil, errors.New("admin: repeated ordering parameter")
	}
	ordering := slices.Clone(options.Ordering)
	if requested := values.Get("o"); requested != "" {
		name := strings.TrimPrefix(requested, "-")
		mapped, ok := userDisplayOrdering(options, name)
		if !ok {
			return nil, errors.New("admin: invalid display ordering")
		}
		if strings.HasPrefix(requested, "-") {
			mapped = reverseOrdering(mapped)
		}
		ordering = []string{mapped}
	}
	for _, key := range options.Schema.PKFields() {
		if !slices.Contains(ordering, key.Name) && !slices.Contains(ordering, "-"+key.Name) {
			ordering = append(ordering, key.Name)
		}
	}
	return ordering, nil
}

func listSortLink(options ModelAdmin, name string, query url.Values, ordering []string) (string, string) {
	mapped, allowed := userDisplayOrdering(options, name)
	if !allowed {
		return "", "none"
	}
	direction, next := "none", name
	if len(ordering) > 0 && activeSortColumn(options, query, ordering) == name {
		if ordering[0] == mapped {
			next = "-" + name
		} else if ordering[0] == reverseOrdering(mapped) {
			next = name
		}
		if ordering[0] == mapped || ordering[0] == reverseOrdering(mapped) {
			direction = "ascending"
			if strings.HasPrefix(ordering[0], "-") {
				direction = "descending"
			}
		}
	}
	values := url.Values{}
	for key, items := range query {
		values[key] = slices.Clone(items)
	}
	values.Set("o", next)
	values.Del("p")
	return "?" + values.Encode(), direction
}

// Two display columns can use the same stored field. Only the selected header
// announces the ordering; default ordering chooses the first visible match.
func activeSortColumn(options ModelAdmin, query url.Values, ordering []string) string {
	if requested := query.Get("o"); requested != "" {
		return strings.TrimPrefix(requested, "-")
	}
	if len(ordering) > 0 {
		for _, name := range options.ListDisplay {
			if mapped, ok := userDisplayOrdering(options, name); ok && (ordering[0] == mapped || ordering[0] == reverseOrdering(mapped)) {
				return name
			}
		}
	}
	return ""
}
