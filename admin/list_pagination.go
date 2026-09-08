package admin

import (
	"errors"
	"net/url"
	"slices"
	"strconv"
)

type listPage struct {
	number, limit, offset int
	all                   bool
}

// The empty all value matches the Admin show-all link. Ambiguous modes never
// silently widen a normal page or discard a supplied page number.
func listPagination(options ModelAdmin, values url.Values) (listPage, error) {
	page := listPage{number: 1, limit: options.ListPerPage}
	if all, exists := values["all"]; exists {
		if len(all) != 1 || all[0] != "" || values.Has("p") {
			return page, errors.New("admin: invalid show-all parameter")
		}
		page.all, page.limit = true, options.ListMaxShowAll+1
		return page, nil
	}
	if len(values["p"]) > 1 {
		return page, errors.New("admin: repeated page parameter")
	}
	if text := values.Get("p"); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil || value < 1 || value > 1000000 {
			return page, errors.New("admin: invalid page")
		}
		page.number = value
	}
	page.offset = (page.number - 1) * page.limit
	return page, nil
}

func listEditableLimit(options ModelAdmin, values url.Values) (int, error) {
	page, err := listPagination(options, values)
	if page.all {
		return options.ListMaxShowAll, err
	}
	return options.ListPerPage, err
}

func listModeURL(query url.Values, all bool) string {
	values := url.Values{}
	for key, items := range query {
		values[key] = slices.Clone(items)
	}
	values.Del("p")
	values.Del("all")
	if all {
		values.Set("all", "")
	}
	return "?" + values.Encode()
}
