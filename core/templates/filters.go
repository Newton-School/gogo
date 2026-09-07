package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

func builtinFilters() map[string]Filter {
	m := map[string]Filter{}
	text := func(fn func(string, string) string) Filter {
		return func(_ context.Context, v, a any) (any, error) { return fn(display(v), display(a)), nil }
	}
	m["lower"] = text(func(s, _ string) string { return strings.ToLower(s) })
	m["upper"] = text(func(s, _ string) string { return strings.ToUpper(s) })
	m["capfirst"] = text(func(s, _ string) string {
		r := []rune(s)
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
		}
		return string(r)
	})
	m["title"] = text(func(s, _ string) string {
		words := strings.Fields(s)
		for i, w := range words {
			r := []rune(strings.ToLower(w))
			if len(r) > 0 {
				r[0] = unicode.ToUpper(r[0])
			}
			words[i] = string(r)
		}
		return strings.Join(words, " ")
	})
	m["cut"] = text(func(s, a string) string { return strings.ReplaceAll(s, a, "") })
	m["addslashes"] = text(func(s, _ string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`, `"`, `\"`).Replace(s) })
	m["slugify"] = text(func(s, _ string) string {
		return strings.Trim(slugSeparators.ReplaceAllString(slugInvalid.ReplaceAllString(strings.ToLower(s), ""), "-"), "-_")
	})
	m["default"] = func(_ context.Context, v, a any) (any, error) {
		if !truthy(v) {
			return a, nil
		}
		return v, nil
	}
	m["default_if_none"] = func(_ context.Context, v, a any) (any, error) {
		if v == nil {
			return a, nil
		}
		return v, nil
	}
	m["length"] = func(_ context.Context, v, _ any) (any, error) {
		if v == nil {
			return 0, nil
		}
		r := reflect.ValueOf(v)
		switch r.Kind() {
		case reflect.Array, reflect.Slice, reflect.Map:
			return r.Len(), nil
		case reflect.String:
			return len([]rune(r.String())), nil
		}
		return 0, nil
	}
	m["first"] = func(_ context.Context, v, _ any) (any, error) {
		items := sequence(v)
		if len(items) == 0 {
			return "", nil
		}
		return items[0], nil
	}
	m["last"] = func(_ context.Context, v, _ any) (any, error) {
		items := sequence(v)
		if len(items) == 0 {
			return "", nil
		}
		return items[len(items)-1], nil
	}
	m["make_list"] = func(_ context.Context, v, _ any) (any, error) { return sequence(display(v)), nil }
	m["join"] = func(_ context.Context, v, a any) (any, error) {
		items := sequence(v)
		parts := make([]string, len(items))
		for i, item := range items {
			parts[i] = display(item)
		}
		return strings.Join(parts, display(a)), nil
	}
	m["slice"] = func(_ context.Context, v, a any) (any, error) {
		items := sequence(v)
		parts := strings.Split(display(a), ":")
		if len(parts) > 3 {
			return nil, ErrRender
		}
		start, end, step := 0, len(items), 1
		values := []*int{&start, &end, &step}
		for i, part := range parts {
			if part == "" {
				continue
			}
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, ErrRender
			}
			*values[i] = n
		}
		if step == 0 {
			return nil, ErrRender
		}
		if start < 0 {
			start += len(items)
		}
		if end < 0 {
			end += len(items)
		}
		start = max(0, min(start, len(items)))
		end = max(0, min(end, len(items)))
		result := []any{}
		if step > 0 {
			for i := start; i < end; i += step {
				result = append(result, items[i])
			}
		} else {
			if len(parts) > 0 && parts[0] == "" {
				start = len(items) - 1
			}
			if len(parts) > 1 && parts[1] == "" {
				end = -1
			}
			for i := start; i > end && i >= 0 && i < len(items); i += step {
				result = append(result, items[i])
			}
		}
		if _, ok := v.(string); ok {
			parts := []string{}
			for _, item := range result {
				parts = append(parts, display(item))
			}
			return strings.Join(parts, ""), nil
		}
		return result, nil
	}
	m["yesno"] = func(_ context.Context, v, a any) (any, error) {
		choices := []string{"yes", "no", "maybe"}
		if a != nil {
			choices = strings.Split(display(a), ",")
		}
		if len(choices) < 2 {
			return v, nil
		}
		if v == nil && len(choices) > 2 {
			return choices[2], nil
		}
		if truthy(v) {
			return choices[0], nil
		}
		return choices[1], nil
	}
	m["pluralize"] = func(_ context.Context, v, a any) (any, error) {
		choices := []string{"", "s"}
		if a != nil {
			choices = strings.Split(display(a), ",")
			if len(choices) == 1 {
				choices = []string{"", choices[0]}
			}
		}
		if len(choices) != 2 {
			return "", nil
		}
		n, ok := number(v)
		if !ok {
			n = float64(len(sequence(v)))
		}
		if n == 1 {
			return choices[0], nil
		}
		return choices[1], nil
	}
	m["add"] = func(_ context.Context, v, a any) (any, error) {
		x, xe := strconv.ParseInt(display(v), 10, 64)
		y, ye := strconv.ParseInt(display(a), 10, 64)
		if xe == nil && ye == nil {
			return x + y, nil
		}
		if left, ok := v.(string); ok {
			if right, ok := a.(string); ok {
				return left + right, nil
			}
		}
		return "", nil
	}
	m["divisibleby"] = func(_ context.Context, v, a any) (any, error) {
		x, e1 := strconv.ParseInt(display(v), 10, 64)
		y, e2 := strconv.ParseInt(display(a), 10, 64)
		return e1 == nil && e2 == nil && y != 0 && x%y == 0, nil
	}
	m["get_digit"] = func(_ context.Context, v, a any) (any, error) {
		s := display(v)
		n, err := strconv.Atoi(display(a))
		if err != nil || n < 1 {
			return v, nil
		}
		if n > len(s) {
			return 0, nil
		}
		i, err := strconv.Atoi(s[len(s)-n : len(s)-n+1])
		if err != nil {
			return v, nil
		}
		return i, nil
	}
	m["escape"] = func(_ context.Context, v, _ any) (any, error) {
		if safe, ok := v.(SafeHTML); ok {
			return safe, nil
		}
		return SafeHTML(html.EscapeString(display(v))), nil
	}
	m["force_escape"] = func(_ context.Context, v, _ any) (any, error) { return SafeHTML(html.EscapeString(display(v))), nil }
	m["safe"] = func(_ context.Context, v, _ any) (any, error) {
		if safe, ok := v.(SafeHTML); ok {
			return safe, nil
		}
		return nil, ErrRender
	}
	m["escapeseq"] = func(ctx context.Context, v, _ any) (any, error) {
		items := sequence(v)
		for i, item := range items {
			items[i], _ = m["escape"](ctx, item, nil)
		}
		return items, nil
	}
	m["safeseq"] = func(ctx context.Context, v, _ any) (any, error) {
		items := sequence(v)
		for i, item := range items {
			value, err := m["safe"](ctx, item, nil)
			if err != nil {
				return nil, err
			}
			items[i] = value
		}
		return items, nil
	}
	m["linebreaksbr"] = func(_ context.Context, v, _ any) (any, error) {
		return SafeHTML(strings.ReplaceAll(html.EscapeString(display(v)), "\n", "<br>")), nil
	}
	m["linebreaks"] = func(_ context.Context, v, _ any) (any, error) {
		parts := strings.Split(html.EscapeString(display(v)), "\n\n")
		for i, p := range parts {
			parts[i] = "<p>" + strings.ReplaceAll(p, "\n", "<br>") + "</p>"
		}
		return SafeHTML(strings.Join(parts, "\n\n")), nil
	}
	m["striptags"] = text(func(s, _ string) string { return tags.ReplaceAllString(s, "") })
	m["wordcount"] = func(_ context.Context, v, _ any) (any, error) { return len(strings.Fields(display(v))), nil }
	m["truncatechars"] = text(func(s, a string) string {
		n, _ := strconv.Atoi(a)
		r := []rune(s)
		if n < 1 {
			return ""
		}
		if len(r) <= n {
			return s
		}
		return string(r[:n-1]) + "…"
	})
	m["truncatewords"] = text(func(s, a string) string {
		n, _ := strconv.Atoi(a)
		words := strings.Fields(s)
		if n < 1 {
			return ""
		}
		if len(words) <= n {
			return s
		}
		return strings.Join(words[:n], " ") + " …"
	})
	m["linenumbers"] = text(func(s, _ string) string {
		lines := strings.Split(s, "\n")
		width := len(strconv.Itoa(len(lines)))
		for i, line := range lines {
			lines[i] = fmt.Sprintf("%*d. %s", width, i+1, line)
		}
		return strings.Join(lines, "\n")
	})
	for _, name := range []string{"ljust", "rjust", "center"} {
		name := name
		m[name] = text(func(s, a string) string {
			n, _ := strconv.Atoi(a)
			n = min(n, 100000)
			spaces := n - len([]rune(s))
			if spaces <= 0 {
				return s
			}
			switch name {
			case "ljust":
				return s + strings.Repeat(" ", spaces)
			case "rjust":
				return strings.Repeat(" ", spaces) + s
			default:
				return strings.Repeat(" ", spaces/2) + s + strings.Repeat(" ", spaces-spaces/2)
			}
		})
	}
	m["urlencode"] = text(func(s, _ string) string { return url.QueryEscape(s) })
	m["iriencode"] = text(func(s, _ string) string {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		return u.String()
	})
	m["pprint"] = text(func(s, _ string) string { return s })
	m["floatformat"] = func(_ context.Context, v, a any) (any, error) {
		n, err := strconv.ParseFloat(display(v), 64)
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return "", nil
		}
		digits := 1
		if a != nil {
			digits, _ = strconv.Atoi(display(a))
		}
		digits = max(-20, min(digits, 20))
		trim := digits < 0
		if trim {
			digits = -digits
		}
		value := strconv.FormatFloat(n, 'f', digits, 64)
		if trim && strings.Contains(value, ".") {
			value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
		}
		return value, nil
	}
	m["filesizeformat"] = func(_ context.Context, v, _ any) (any, error) {
		n, err := strconv.ParseFloat(display(v), 64)
		if err != nil {
			return "0 bytes", nil
		}
		units := []string{"bytes", "KB", "MB", "GB", "TB", "PB"}
		i := 0
		for n >= 1024 && i < len(units)-1 {
			n /= 1024
			i++
		}
		if i == 0 {
			return fmt.Sprintf("%.0f bytes", n), nil
		}
		return fmt.Sprintf("%.1f %s", n, units[i]), nil
	}
	m["date"] = dateFilter(false)
	m["time"] = dateFilter(true)
	for name, filter := range timezoneFilters() {
		m[name] = filter
	}
	m["dictsort"] = func(_ context.Context, v, a any) (any, error) {
		items := sequence(v)
		sort.SliceStable(items, func(i, j int) bool {
			x, _ := lookup(items[i], display(a))
			y, _ := lookup(items[j], display(a))
			cmp, _ := compare(x, y)
			return cmp < 0
		})
		return items, nil
	}
	m["dictsortreversed"] = func(ctx context.Context, v, a any) (any, error) {
		items, err := m["dictsort"](ctx, v, a)
		if err != nil {
			return nil, err
		}
		result := items.([]any)
		slices.Reverse(result)
		return result, nil
	}
	m["json_script"] = func(_ context.Context, v, a any) (any, error) {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, ErrRender
		}
		id := ""
		if a != nil {
			id = ` id="` + html.EscapeString(display(a)) + `"`
		}
		return SafeHTML(`<script` + id + ` type="application/json">` + string(data) + `</script>`), nil
	}
	return m
}
func display(v any) string {
	if v == nil {
		return ""
	}
	if value, ok := v.(zonedTime); ok {
		return value.instant.String()
	}
	return fmt.Sprint(v)
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9_\s-]`)
var slugSeparators = regexp.MustCompile(`[-\s]+`)
var tags = regexp.MustCompile(`<[^>]*>`)
