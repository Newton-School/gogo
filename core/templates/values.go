package templates

import (
	"html/template"
	"net/url"
	"reflect"
	"time"
)

// projectValue copies exported data without invoking Stringer or other methods.
func projectValue(value any, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrRender
	}
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case SafeHTML, template.HTML, time.Time:
		return v, nil
	case url.Values:
		result := url.Values{}
		for k, items := range v {
			result[k] = append([]string(nil), items...)
		}
		return result, nil
	}
	v := reflect.ValueOf(value)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
		depth++
		if depth > 64 {
			return nil, ErrRender
		}
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	case reflect.Struct:
		result := Context{}
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			item, err := projectValue(v.Field(i).Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			result[field.Name] = item
		}
		return result, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, ErrRender
		}
		result := Context{}
		iter := v.MapRange()
		for iter.Next() {
			item, err := projectValue(iter.Value().Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			result[iter.Key().String()] = item
		}
		return result, nil
	case reflect.Slice, reflect.Array:
		result := make([]any, v.Len())
		for i := range result {
			item, err := projectValue(v.Index(i).Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			result[i] = item
		}
		return result, nil
	default:
		return nil, ErrRender
	}
}
