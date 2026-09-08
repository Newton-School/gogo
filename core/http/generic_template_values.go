package http

import (
	htmltemplate "html/template"
	"math"
	"net/url"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/templates"
)

const (
	templateContextMaxDepth  = 32
	templateContextMaxValues = 65_536
	templateContextMaxBytes  = 8 << 20
)

// snapshotTemplateContext freezes generic-view data before processors/loaders
// can run. This is deliberately separate from the engine's projection policy:
// it neither invokes user methods nor retains their receiver objects.
func snapshotTemplateContext(values templates.Context) (result templates.Context, err error) {
	defer func() {
		if recover() != nil {
			result, err = nil, ErrUnavailable
		}
	}()
	budget := templateContextBudget{remainingValues: templateContextMaxValues, remainingBytes: templateContextMaxBytes}
	value, ok := budget.copy(reflect.ValueOf(values), 0)
	if !ok {
		return nil, ErrUnavailable
	}
	if value == nil {
		return nil, nil
	}
	return value.(templates.Context), nil
}

type templateContextBudget struct {
	remainingValues int
	remainingBytes  int
}

func (b *templateContextBudget) visit(depth int) bool {
	if depth > templateContextMaxDepth || b.remainingValues == 0 {
		return false
	}
	b.remainingValues--
	return true
}

func (b *templateContextBudget) text(value string) bool {
	if len(value) > b.remainingBytes || !utf8.ValidString(value) {
		return false
	}
	b.remainingBytes -= len(value)
	return true
}

func (b *templateContextBudget) key(value string, depth int) bool {
	return b.visit(depth) && b.text(value)
}

func (b *templateContextBudget) copy(value reflect.Value, depth int) (any, bool) {
	if !b.visit(depth) {
		return nil, false
	}
	if !value.IsValid() {
		return nil, true
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, true
		}
		return b.copy(value.Elem(), depth+1)
	}
	// Only these exact trusted scalar/collection types retain their identity.
	// Defined string aliases lose HTML safety provenance; no Stringer, marshaler
	// or arbitrary reflection method is used to inspect any supplied value.
	if value.CanInterface() {
		switch scalar := value.Interface().(type) {
		case templates.SafeHTML:
			return scalar, b.text(string(scalar))
		case htmltemplate.HTML:
			return scalar, b.text(string(scalar))
		case time.Time:
			// Match templates.copyTemplateTime: initialize standard-library Local
			// once, then detach the publicly mutable Location pointer. Its private
			// transition tables are immutable through the supported time API.
			zone := scalar.Location()
			if !b.text(zone.String()) {
				return nil, false
			}
			copiedZone := *zone
			return scalar.In(&copiedZone), true
		case url.Values:
			return b.queryValues(scalar, depth)
		}
	}
	switch value.Kind() {
	case reflect.Bool:
		return value.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	case reflect.String:
		text := value.String()
		return text, b.text(text)
	case reflect.Struct:
		// Even skipped private fields consume traversal work, but their contents
		// and names are never exposed or inspected. Preflight before allocation.
		if value.NumField() > b.remainingValues {
			return nil, false
		}
		result := templates.Context{}
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if !field.IsExported() {
				if !b.visit(depth + 1) {
					return nil, false
				}
				continue
			}
			if !b.key(field.Name, depth+1) {
				return nil, false
			}
			item, ok := b.copy(value.Field(i), depth+1)
			if !ok {
				return nil, false
			}
			result[field.Name] = item
		}
		return result, true
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String || value.Len() > b.remainingValues/2 {
			return nil, false
		}
		if value.IsNil() {
			return nil, true
		}
		result := make(templates.Context, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			if !b.key(key, depth+1) {
				return nil, false
			}
			item, ok := b.copy(iterator.Value(), depth+1)
			if !ok {
				return nil, false
			}
			result[key] = item
		}
		return result, true
	case reflect.Slice, reflect.Array:
		if value.Len() > b.remainingValues {
			return nil, false
		}
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil, true
		}
		result := make([]any, value.Len())
		for i := range result {
			item, ok := b.copy(value.Index(i), depth+1)
			if !ok {
				return nil, false
			}
			result[i] = item
		}
		return result, true
	default:
		return nil, false
	}
}

func (b *templateContextBudget) queryValues(values url.Values, depth int) (any, bool) {
	if len(values) > b.remainingValues/2 {
		return nil, false
	}
	if values == nil {
		return url.Values(nil), true
	}
	result := make(url.Values, len(values))
	for key, items := range values {
		if !b.key(key, depth+1) || !b.visit(depth+1) || len(items) > b.remainingValues {
			return nil, false
		}
		var copied []string
		if items != nil {
			copied = make([]string, len(items))
		}
		for i, item := range items {
			if !b.visit(depth+2) || !b.text(item) {
				return nil, false
			}
			copied[i] = item
		}
		result[key] = copied
	}
	return result, true
}
