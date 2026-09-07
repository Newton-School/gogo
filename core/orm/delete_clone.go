package orm

import (
	"reflect"
	"time"

	"github.com/Newton-School/gogo/core/models"
)

type deleteClone struct {
	seen      map[cloneVisit]reflect.Value
	records   map[models.Record]*deleteRecordView
	nodes     int
	bytes     int
	locations map[*time.Location]*time.Location
}

func (c *deleteClone) reserveBytes(count int, size uintptr) bool {
	if count == 0 {
		return true
	}
	remaining := (64 << 20) - c.bytes
	if remaining < 0 || size > uintptr(remaining) || (size > 0 && count > remaining/int(size)) {
		return false
	}
	c.bytes += count * int(size)
	return true
}

func (c *deleteClone) value(value any) (any, error) {
	copy, err := c.reflectValue(reflect.ValueOf(value), 0)
	if err != nil || !copy.IsValid() {
		return nil, err
	}
	return copy.Interface(), nil
}

func (c *deleteClone) reflectValue(value reflect.Value, depth int) (reflect.Value, error) {
	c.nodes++
	if depth > 128 || c.nodes > 1<<20 {
		return reflect.Value{}, ErrDeleteCallbackView
	}
	if !value.IsValid() {
		return value, nil
	}
	if value.Type() == reflect.TypeFor[time.Time]() {
		if !value.CanInterface() {
			return reflect.Value{}, ErrDeleteCallbackView
		}
		timestamp := value.Interface().(time.Time)
		location := timestamp.Location()
		copy := c.locations[location]
		if copy == nil {
			if !c.reserveBytes(1, reflect.TypeFor[time.Location]().Size()) {
				return reflect.Value{}, ErrDeleteCallbackView
			}
			// Local's lazy initialization must finish before its opaque immutable
			// internals are copied. No application method is invoked.
			_ = location.String()
			copy = new(time.Location)
			*copy = *location
			if c.locations == nil {
				c.locations = map[*time.Location]*time.Location{}
			}
			c.locations[location] = copy
		}
		// In preserves the instant and zone rules, while intentionally dropping
		// process-local monotonic metadata from the callback snapshot.
		return reflect.ValueOf(timestamp.In(copy)), nil
	}
	if c.seen == nil {
		c.seen = map[cloneVisit]reflect.Value{}
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		child, err := c.reflectValue(value.Elem(), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(child)
		return result, nil
	case reflect.Pointer, reflect.Slice, reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		key := cloneVisit{kind: value.Kind(), typ: value.Type(), pointer: uintptr(value.UnsafePointer())}
		if value.Kind() == reflect.Slice {
			key.length = value.Len()
		}
		if previous, ok := c.seen[key]; ok {
			return previous, nil
		}
		var result reflect.Value
		switch value.Kind() {
		case reflect.Pointer:
			if !c.reserveBytes(1, value.Type().Elem().Size()) {
				return reflect.Value{}, ErrDeleteCallbackView
			}
			result = reflect.New(value.Type().Elem())
			if result.Type() != value.Type() {
				result = result.Convert(value.Type())
			}
			c.seen[key] = result
			child, err := c.reflectValue(value.Elem(), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Elem().Set(child)
		case reflect.Slice:
			if !c.reserveBytes(value.Len(), value.Type().Elem().Size()) {
				return reflect.Value{}, ErrDeleteCallbackView
			}
			if value.Type().Elem().Kind() == reflect.Uint8 {
				result = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
				c.seen[key] = result
				reflect.Copy(result, value)
				return result, nil
			}
			if value.Len() > (1<<20)-c.nodes {
				return reflect.Value{}, ErrDeleteCallbackView
			}
			result = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			c.seen[key] = result
			for i := range value.Len() {
				child, err := c.reflectValue(value.Index(i), depth+1)
				if err != nil {
					return reflect.Value{}, err
				}
				result.Index(i).Set(child)
			}
		case reflect.Map:
			if value.Len() > ((1<<20)-c.nodes)/2 {
				return reflect.Value{}, ErrDeleteCallbackView
			}
			if !c.reserveBytes(value.Len(), value.Type().Key().Size()+value.Type().Elem().Size()+32) {
				return reflect.Value{}, ErrDeleteCallbackView
			}
			result = reflect.MakeMapWithSize(value.Type(), value.Len())
			c.seen[key] = result
			iter := value.MapRange()
			for iter.Next() {
				mapKey, err := c.reflectValue(iter.Key(), depth+1)
				if err != nil {
					return reflect.Value{}, err
				}
				child, err := c.reflectValue(iter.Value(), depth+1)
				if err != nil {
					return reflect.Value{}, err
				}
				// Timestamp normalization can make formerly distinct Go map keys
				// equal (for example, the same wall time with/without monotonic
				// metadata). Never silently discard either entry in a view.
				if result.MapIndex(mapKey).IsValid() {
					return reflect.Value{}, ErrDeleteCallbackView
				}
				result.SetMapIndex(mapKey, child)
			}
		}
		return result, nil
	case reflect.Struct:
		if !c.reserveBytes(1, value.Type().Size()) {
			return reflect.Value{}, ErrDeleteCallbackView
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for i := range value.NumField() {
			if !value.Type().Field(i).IsExported() {
				if !c.immutableField(value.Field(i), depth+1) {
					return reflect.Value{}, ErrDeleteCallbackView
				}
				continue
			}
			child, err := c.reflectValue(value.Field(i), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Field(i).Set(child)
		}
		return result, nil
	case reflect.Array:
		if !c.reserveBytes(1, value.Type().Size()) {
			return reflect.Value{}, ErrDeleteCallbackView
		}
		result := reflect.New(value.Type()).Elem()
		for i := range value.Len() {
			child, err := c.reflectValue(value.Index(i), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(i).Set(child)
		}
		return result, nil
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		if !value.IsNil() {
			return reflect.Value{}, ErrDeleteCallbackView
		}
	}
	return value, nil
}

func (c *deleteClone) immutableField(value reflect.Value, depth int) bool {
	c.nodes++
	if depth > 128 || c.nodes > 1<<20 {
		return false
	}
	if value.Type() == reflect.TypeFor[time.Time]() {
		// A private time field cannot be replaced with a detached Location via
		// safe reflection; its public accessor could otherwise expose an alias.
		return false
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := range value.NumField() {
			if !c.immutableField(value.Field(i), depth+1) {
				return false
			}
		}
	case reflect.Array:
		for i := range value.Len() {
			if !c.immutableField(value.Index(i), depth+1) {
				return false
			}
		}
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return value.IsNil()
	}
	return true
}

func (c *deleteClone) schema(schema models.Schema) (models.Schema, error) {
	schema = schema.Clone()
	var cloneField func(*models.Field) error
	cloneField = func(field *models.Field) error {
		for _, target := range []*any{&field.Min, &field.Max, &field.Default} {
			value, err := c.value(*target)
			if err != nil {
				return err
			}
			*target = value
		}
		for i := range field.Choices {
			value, err := c.value(field.Choices[i].Value)
			if err != nil {
				return err
			}
			field.Choices[i].Value = value
		}
		if field.Element != nil {
			return cloneField(field.Element)
		}
		return nil
	}
	for i := range schema.Fields {
		if err := cloneField(&schema.Fields[i]); err != nil {
			return models.Schema{}, err
		}
	}
	return schema, nil
}

func (c *deleteClone) record(record models.Record) (*deleteRecordView, error) {
	if c.records == nil {
		c.records = map[models.Record]*deleteRecordView{}
	}
	if previous := c.records[record]; previous != nil {
		return previous, nil
	}
	schema, err := c.schema(record.Schema())
	if err != nil {
		return nil, err
	}
	view := &deleteRecordView{schema: schema, values: map[string]any{}}
	c.records[record] = view
	for _, field := range schema.Fields {
		if field.IsStored() {
			value, err := record.Get(field.Name)
			if err != nil {
				return nil, err
			}
			view.values[field.Name], err = c.value(value)
			if err != nil {
				return nil, err
			}
		}
	}
	state, err := c.value(*record.State())
	if err != nil {
		return nil, err
	}
	view.state = state.(models.State)
	return view, nil
}
