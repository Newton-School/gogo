package templates

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (r *renderer) eval(expression string, data Context) (any, error) {
	parts := splitQuoted(expression, '|')
	if len(parts) == 0 {
		return nil, nil
	}
	value, err := r.atom(parts[0], data)
	if err != nil {
		return nil, err
	}
	for _, part := range parts[1:] {
		pair := splitQuoted(part, ':')
		if len(pair) > 2 || len(pair) == 0 {
			return nil, ErrRender
		}
		filter, ok := r.engine.config.Filters[pair[0]]
		if !ok {
			return nil, fmt.Errorf("templates: unknown filter %s", pair[0])
		}
		var arg any
		if len(pair) == 2 {
			arg, err = r.atom(pair[1], data)
			if err != nil {
				return nil, err
			}
		}
		value, err = filter(r.ctx, value, arg)
		if err != nil {
			return nil, ErrRender
		}
	}
	return value, nil
}
func (r *renderer) atom(s string, data Context) (any, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"')) {
		return strings.ReplaceAll(strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`), `\'`, `'`), nil
	}
	switch s {
	case "True", "true":
		return true, nil
	case "False", "false":
		return false, nil
	case "None", "nil", "null":
		return nil, nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	var current any = data
	for _, piece := range strings.Split(s, ".") {
		if piece == "" || strings.HasPrefix(piece, "_") {
			return nil, ErrRender
		}
		value, ok := lookup(current, piece)
		if !ok {
			if r.engine.config.Strict {
				return nil, fmt.Errorf("templates: variable not found")
			}
			return nil, nil
		}
		current = value
	}
	return current, nil
}
func lookup(value any, name string) (any, bool) {
	if value == nil {
		return nil, false
	}
	if m, ok := value.(Context); ok {
		v, ok := m[name]
		return v, ok
	}
	if m, ok := value.(map[string]any); ok {
		v, ok := m[name]
		return v, ok
	}
	v := reflect.ValueOf(value)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		key := reflect.ValueOf(name).Convert(v.Type().Key())
		item := v.MapIndex(key)
		if item.IsValid() {
			return safeValue(item)
		}
	case reflect.Struct:
		field := v.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			return safeValue(field)
		}
	case reflect.Slice, reflect.Array, reflect.String:
		i, err := strconv.Atoi(name)
		if err == nil && i >= 0 && i < v.Len() {
			return safeValue(v.Index(i))
		}
	}
	return nil, false
}
func safeValue(v reflect.Value) (any, bool) {
	if !v.IsValid() || !v.CanInterface() {
		return nil, false
	}
	switch v.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return nil, false
	}
	return v.Interface(), true
}
func truthy(value any) bool {
	if value == nil {
		return false
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.String, reflect.Array, reflect.Slice, reflect.Map:
		return v.Len() > 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return v.Float() != 0
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil()
	}
	return true
}
func (r *renderer) condition(expression string, data Context) (bool, error) {
	words := splitQuoted(expression, ' ')
	for _, op := range []string{"or", "and"} {
		for i, word := range words {
			if word == op {
				left, err := r.condition(strings.Join(words[:i], " "), data)
				if err != nil {
					return false, err
				}
				if op == "or" && left {
					return true, nil
				}
				if op == "and" && !left {
					return false, nil
				}
				return r.condition(strings.Join(words[i+1:], " "), data)
			}
		}
	}
	if len(words) > 0 && words[0] == "not" {
		v, err := r.condition(strings.Join(words[1:], " "), data)
		return !v, err
	}
	for i, word := range words {
		if word == "==" || word == "!=" || word == "<" || word == ">" || word == "<=" || word == ">=" || word == "in" || word == "is" {
			leftWords := words[:i]
			negated := false
			if len(leftWords) > 0 && leftWords[len(leftWords)-1] == "not" {
				negated = true
				leftWords = leftWords[:len(leftWords)-1]
			}
			rightWords := words[i+1:]
			if word == "is" && len(rightWords) > 0 && rightWords[0] == "not" {
				negated = true
				rightWords = rightWords[1:]
			}
			left, err := r.eval(strings.Join(leftWords, " "), data)
			if err != nil {
				return false, err
			}
			right, err := r.eval(strings.Join(rightWords, " "), data)
			if err != nil {
				return false, err
			}
			result := false
			switch word {
			case "==", "is":
				result = reflect.DeepEqual(left, right)
			case "!=":
				result = !reflect.DeepEqual(left, right)
			case "in":
				result = contains(right, left)
			default:
				cmp, ok := compare(left, right)
				if !ok {
					return false, nil
				}
				switch word {
				case "<":
					result = cmp < 0
				case ">":
					result = cmp > 0
				case "<=":
					result = cmp <= 0
				case ">=":
					result = cmp >= 0
				}
			}
			if negated {
				result = !result
			}
			return result, nil
		}
	}
	v, err := r.eval(expression, data)
	return truthy(v), err
}
func compare(a, b any) (int, bool) {
	if x, ok := number(a); ok {
		if y, ok := number(b); ok {
			if x < y {
				return -1, true
			}
			if x > y {
				return 1, true
			}
			return 0, true
		}
	}
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), true
		}
	}
	if x, ok := a.(time.Time); ok {
		if y, ok := b.(time.Time); ok {
			return x.Compare(y), true
		}
	}
	return 0, false
}
func contains(collection, item any) bool {
	if s, ok := collection.(string); ok {
		return strings.Contains(s, fmt.Sprint(item))
	}
	v := reflect.ValueOf(collection)
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if reflect.DeepEqual(key.Interface(), item) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if reflect.DeepEqual(v.Index(i).Interface(), item) {
				return true
			}
		}
	}
	return false
}
func number(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(r.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(r.Uint()), true
	case reflect.Float32, reflect.Float64:
		return r.Float(), true
	}
	return 0, false
}
func sequence(value any) []any {
	if value == nil {
		return nil
	}
	v := reflect.ValueOf(value)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	result := []any{}
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if item, ok := safeValue(v.Index(i)); ok {
				result = append(result, item)
			}
		}
	case reflect.String:
		for _, ch := range v.String() {
			result = append(result, string(ch))
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, key := range keys {
			result = append(result, key.Interface())
		}
	}
	return result
}
