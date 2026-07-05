package admin

import (
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidChangeListQuery = errors.New("invalid change list query")

// ComputedColumn renders a computed value for a row.
type ComputedColumn func(map[string]any) any

// ChangeList is render-ready list page data.
type ChangeList struct {
	Columns          []ChangeListColumn
	Rows             []ChangeListRow
	Total            int
	Page             int
	PerPage          int
	ShowAll          bool
	CanShowAll       bool
	BulkSelection    bool
	Popup            bool
	PreservedFilters string
	DateHierarchy    []DateBucket
}

// ChangeListColumn describes one list column.
type ChangeListColumn struct {
	Name     string
	Editable bool
	Computed bool
	Link     bool
}

// Label returns the Django-style column header label.
func (c ChangeListColumn) Label() string {
	switch c.Name {
	case "email":
		return "Email address"
	case "first_name":
		return "First name"
	case "last_name":
		return "Last name"
	case "is_staff":
		return "Staff status"
	case "is_active":
		return "Active"
	case "is_superuser":
		return "Superuser status"
	}
	return adminLabel(c.Name)
}

// ChangeListRow stores rendered values for one object.
type ChangeListRow struct {
	ObjectID string
	Object   map[string]any
	Values   map[string]any
	Cells    []ChangeListCell
}

// ChangeListCell stores render-ready data for one row/column intersection.
type ChangeListCell struct {
	Name    string
	Class   string
	Value   any
	LinkURL string
}

// DateBucket stores date hierarchy counts by year.
type DateBucket struct {
	Year  int
	Count int
}

// BuildChangeList builds Django-style change-list context from row data.
func BuildChangeList(admin ModelAdmin, rows []map[string]any, query url.Values) (ChangeList, error) {
	options := admin.Normalize()
	if len(options.ListDisplay) == 0 {
		options.ListDisplay = []string{"__str__"}
	}
	copiedRows := cloneRows(rows)
	if err := sortRows(copiedRows, options, query.Get("o")); err != nil {
		return ChangeList{}, err
	}
	page, err := pageNumber(query.Get("p"))
	if err != nil {
		return ChangeList{}, err
	}
	total := len(copiedRows)
	canShowAll := total <= options.ListMaxShowAll
	showAll := query.Get("all") == "1" && canShowAll
	perPage := options.ListPerPage
	pageRows := copiedRows
	if !showAll {
		pageRows = paginateRows(copiedRows, page, perPage)
	}
	return ChangeList{
		Columns:          buildColumns(options),
		Rows:             buildDisplayRows(options, pageRows),
		Total:            total,
		Page:             page,
		PerPage:          perPage,
		ShowAll:          showAll,
		CanShowAll:       canShowAll,
		BulkSelection:    hasConfiguredActions(options),
		Popup:            query.Get("_popup") == "1",
		PreservedFilters: preservedFilters(query),
		DateHierarchy:    buildDateHierarchy(options, copiedRows),
	}, nil
}

func hasConfiguredActions(admin ModelAdmin) bool {
	return len(admin.Actions) > 0 || len(admin.ActionDefinitions) > 0
}

func buildColumns(admin ModelAdmin) []ChangeListColumn {
	editable := setFromSlice(admin.ListEditable)
	links := listDisplayLinkSet(admin)
	columns := make([]ChangeListColumn, len(admin.ListDisplay))
	for i, name := range admin.ListDisplay {
		_, computed := admin.ComputedColumns[name]
		_, isEditable := editable[name]
		_, isLink := links[name]
		columns[i] = ChangeListColumn{Name: name, Editable: isEditable, Computed: computed, Link: isLink}
	}
	return columns
}

func buildDisplayRows(admin ModelAdmin, rows []map[string]any) []ChangeListRow {
	links := listDisplayLinkSet(admin)
	result := make([]ChangeListRow, len(rows))
	for i, row := range rows {
		objectID := objectIDFromRow(row)
		values := make(map[string]any, len(admin.ListDisplay))
		cells := make([]ChangeListCell, 0, len(admin.ListDisplay))
		for _, column := range admin.ListDisplay {
			value := row[column]
			if computed, ok := admin.ComputedColumns[column]; ok {
				value = computed(row)
			}
			display := displayValue(column, value, admin.EmptyValueDisplay)
			values[column] = display
			cell := ChangeListCell{
				Name:  column,
				Class: "field-" + column,
				Value: display,
			}
			if _, ok := links[column]; ok && objectID != "" {
				cell.LinkURL = objectID + "/change/"
			}
			cells = append(cells, cell)
		}
		result[i] = ChangeListRow{ObjectID: objectID, Object: cloneRow(row), Values: values, Cells: cells}
	}
	return result
}

func listDisplayLinkSet(admin ModelAdmin) map[string]struct{} {
	if len(admin.ListDisplayLinks) > 0 {
		return setFromSlice(admin.ListDisplayLinks)
	}
	editable := setFromSlice(admin.ListEditable)
	for _, column := range admin.ListDisplay {
		if _, ok := editable[column]; !ok {
			return map[string]struct{}{column: {}}
		}
	}
	return map[string]struct{}{}
}

func objectIDFromRow(row map[string]any) string {
	for _, key := range []string{"id", "pk"} {
		if value, ok := row[key]; ok && value != nil {
			text := fmt.Sprint(value)
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func sortRows(rows []map[string]any, admin ModelAdmin, ordering string) error {
	if ordering == "" {
		return nil
	}
	desc := strings.HasPrefix(ordering, "-")
	field := strings.TrimPrefix(ordering, "-")
	if _, ok := setFromSlice(admin.ListDisplay)[field]; !ok {
		return fmt.Errorf("%w: unsupported ordering %s", ErrInvalidChangeListQuery, ordering)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := fmt.Sprint(rows[i][field]), fmt.Sprint(rows[j][field])
		if left == "" && right != "" {
			return false
		}
		if right == "" && left != "" {
			return true
		}
		if desc {
			return left > right
		}
		return left < right
	})
	return nil
}

func pageNumber(value string) (int, error) {
	if value == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 {
		return 0, fmt.Errorf("%w: invalid page %q", ErrInvalidChangeListQuery, value)
	}
	return page, nil
}

func paginateRows(rows []map[string]any, page, perPage int) []map[string]any {
	if perPage <= 0 {
		perPage = 100
	}
	start := (page - 1) * perPage
	if start >= len(rows) {
		return nil
	}
	end := start + perPage
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end]
}

func displayValue(field string, value any, empty string) any {
	if isBooleanAdminField(field, value) {
		return BooleanIcon(widgetBool(value))
	}
	switch typed := value.(type) {
	case bool:
		return BooleanIcon(typed)
	case string:
		if typed == "" {
			return empty
		}
		return typed
	case nil:
		return empty
	default:
		return value
	}
}

// BooleanIcon renders a stable boolean display marker.
func BooleanIcon(value bool) template.HTML {
	if value {
		return template.HTML(`<img src="/admin/static/admin/img/icon-yes.svg" alt="True">`)
	}
	return template.HTML(`<img src="/admin/static/admin/img/icon-no.svg" alt="False">`)
}

func preservedFilters(values url.Values) string {
	copied := url.Values{}
	for key, raw := range values {
		switch key {
		case "o", "p", "all", "_popup":
			continue
		default:
			copied[key] = append([]string(nil), raw...)
		}
	}
	return copied.Encode()
}

func buildDateHierarchy(admin ModelAdmin, rows []map[string]any) []DateBucket {
	if admin.DateHierarchy == "" {
		return nil
	}
	counts := map[int]int{}
	for _, row := range rows {
		switch value := row[admin.DateHierarchy].(type) {
		case time.Time:
			counts[value.Year()]++
		}
	}
	years := make([]int, 0, len(counts))
	for year := range counts {
		years = append(years, year)
	}
	sort.Ints(years)
	buckets := make([]DateBucket, len(years))
	for i, year := range years {
		buckets[i] = DateBucket{Year: year, Count: counts[year]}
	}
	return buckets
}

func cloneRows(rows []map[string]any) []map[string]any {
	copied := make([]map[string]any, len(rows))
	for i, row := range rows {
		copied[i] = cloneRow(row)
	}
	return copied
}

func cloneRow(row map[string]any) map[string]any {
	copied := make(map[string]any, len(row))
	for key, value := range row {
		copied[key] = value
	}
	return copied
}
