package admin

import "reflect"

func cloneDisplayText(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Inspect only value shape. In particular, never call String, Len, a relation
// resolver, or any other application method to decide whether to show text.
func emptyDisplayValue(value any) bool {
	v := reflect.ValueOf(value)
	for depth := 0; depth < 64; depth++ {
		if !v.IsValid() {
			return true
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return true
			}
			v = v.Elem()
		case reflect.String, reflect.Array, reflect.Slice, reflect.Map:
			return v.Len() == 0
		case reflect.Chan, reflect.Func:
			return v.IsNil()
		default:
			return false
		}
	}
	// A cyclic/excessively nested pointer is not an empty collection. Do not
	// recursively inspect it or traverse collection elements.
	return false
}

func (s *Site) displayValue(options ModelAdmin, name string, value any) any {
	if !emptyDisplayValue(value) {
		return value
	}
	placeholder := s.config.EmptyValueDisplay
	if options.EmptyValueDisplay != nil {
		placeholder = options.EmptyValueDisplay
	}
	for _, column := range options.Columns {
		if column.Name == name && column.EmptyValueDisplay != nil {
			placeholder = column.EmptyValueDisplay
			break
		}
	}
	if placeholder == nil {
		return "—"
	}
	// Always return ordinary text, never template.HTML or another safe marker.
	return *placeholder
}
