package models

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// Base can be embedded in application structs. The zero value is a new model.
// Instances are owned by one request/goroutine unless callers synchronize access.
type Base struct{ state State }

func (b *Base) ModelState() *State { return &b.state }

type State struct {
	Persisted bool
	Database  string
	Deferred  map[string]bool
	Related   map[string]any
	// Annotations contains explicitly selected query expressions. These values
	// are per-instance snapshots, never model fields or persistence inputs.
	Annotations     map[string]any
	DefaultsApplied bool
	// Provided distinguishes explicitly bound zero values from omitted values.
	Provided map[string]bool
}

func (s *State) Adding() bool { return !s.Persisted }

type Model interface {
	Schema() Schema
	ModelState() *State
}
type Record interface {
	Schema() Schema
	State() *State
	Get(string) (any, error)
	Set(string, any) error
}

// Underlying returns the typed model behind a Bind record. Custom Record
// implementations can expose Model() Model to participate in persistence.
func Underlying(record Record) (Model, bool) {
	bound, ok := record.(interface{ Model() Model })
	if !ok {
		return nil, false
	}
	return bound.Model(), true
}

type BoundRecord struct {
	model  Model
	value  reflect.Value
	schema Schema
}

func Bind(model Model) (Record, error) {
	if model == nil {
		return nil, errors.New("models: nil model")
	}
	v := reflect.ValueOf(model)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, errors.New("models: nil model")
	}
	if record, ok := model.(Record); ok {
		if record.State() == nil {
			return nil, errors.New("models: model state is nil")
		}
		if err := record.Schema().Validate(); err != nil {
			return nil, err
		}
		return record, nil
	}
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil, errors.New("models: model must be nonnil struct pointer")
	}
	if model.ModelState() == nil {
		return nil, errors.New("models: model state is nil")
	}
	schema := model.Schema().Clone()
	if err := schema.Validate(); err != nil {
		return nil, err
	}
	for _, f := range schema.Fields {
		if !f.IsStored() {
			continue
		}
		field := v.Elem().FieldByName(f.GoField())
		if !field.IsValid() || !field.CanSet() {
			return nil, fmt.Errorf("models: missing exported Go field %s", f.GoField())
		}
	}
	return &BoundRecord{model: model, value: v.Elem(), schema: schema}, nil
}
func (r *BoundRecord) Model() Model   { return r.model }
func (r *BoundRecord) Schema() Schema { return r.schema.Clone() }
func (r *BoundRecord) State() *State  { return r.model.ModelState() }
func (r *BoundRecord) Get(name string) (any, error) {
	f, ok := r.schema.Field(name)
	if !ok {
		return nil, fmt.Errorf("models: unknown field %s", name)
	}
	if r.State().Deferred[name] {
		return nil, fmt.Errorf("models: field %s is deferred; fetch it explicitly", name)
	}
	v := r.value.FieldByName(f.GoField())
	if !v.IsValid() || !v.CanInterface() {
		return nil, fmt.Errorf("models: unreadable field %s", name)
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	if f.Kind == JSON && v.Type() == reflect.TypeFor[json.RawMessage]() && v.IsNil() {
		return nil, nil
	}
	return v.Interface(), nil
}
func (r *BoundRecord) Set(name string, value any) error {
	f, ok := r.schema.Field(name)
	if !ok {
		return fmt.Errorf("models: unknown field %s", name)
	}
	v := r.value.FieldByName(f.GoField())
	if !v.IsValid() || !v.CanSet() {
		return fmt.Errorf("models: unwritable field %s", name)
	}
	jsonType := v.Type()
	for jsonType.Kind() == reflect.Pointer {
		jsonType = jsonType.Elem()
	}
	if f.Kind == JSON && jsonType == reflect.TypeFor[json.RawMessage]() && value != nil {
		incoming := reflect.ValueOf(value)
		incomingType := incoming.Type()
		for incomingType.Kind() == reflect.Pointer {
			incomingType = incomingType.Elem()
		}
		if incomingType == reflect.TypeFor[json.RawMessage]() {
			for incoming.Kind() == reflect.Pointer && !incoming.IsNil() {
				incoming = incoming.Elem()
			}
			if incoming.IsNil() {
				value = nil
			} else {
				value = incoming.Interface()
			}
		}
	}
	if f.Kind == JSON && jsonType == reflect.TypeFor[json.RawMessage]() && value != nil {
		var encoded []byte
		var err error
		switch raw := value.(type) {
		case json.RawMessage:
			encoded = append([]byte(nil), raw...)
		case []byte:
			encoded = append([]byte(nil), raw...)
		default:
			encoded, err = json.Marshal(value)
		}
		if err != nil || !json.Valid(encoded) {
			return fmt.Errorf("models: set %s: invalid JSON value", name)
		}
		value = json.RawMessage(encoded)
	}
	var assignmentErr error
	if f.Kind == JSON && jsonType != reflect.TypeFor[json.RawMessage]() && (jsonType.Kind() == reflect.Slice || jsonType.Kind() == reflect.Map) && value != nil {
		assignmentErr = assignJSONContainer(v, value)
	} else {
		assignmentErr = assign(v, value)
	}
	if assignmentErr != nil {
		return fmt.Errorf("models: set %s: %w", name, assignmentErr)
	}
	delete(r.State().Deferred, name)
	if r.State().Provided == nil {
		r.State().Provided = map[string]bool{}
	}
	r.State().Provided[name] = true
	return nil
}

func assign(dst reflect.Value, value any) error {
	if value == nil {
		switch dst.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
			dst.SetZero()
			return nil
		}
		if dst.CanAddr() {
			if scanner, ok := dst.Addr().Interface().(sql.Scanner); ok {
				return scanner.Scan(nil)
			}
		}
		return errors.New("cannot assign NULL to nonnullable Go field")
	}
	if dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		return assign(dst.Elem(), value)
	}
	if dst.CanAddr() {
		if scanner, ok := dst.Addr().Interface().(sql.Scanner); ok {
			return scanner.Scan(value)
		}
	}
	src := reflect.ValueOf(value)
	if src.Type().AssignableTo(dst.Type()) {
		dst.Set(src)
		return nil
	}
	text := ""
	switch v := value.(type) {
	case []byte:
		text = string(v)
	case string:
		text = v
	default:
		text = fmt.Sprint(v)
	}
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(text)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(text, 10, dst.Type().Bits())
		if err == nil {
			dst.SetInt(n)
		}
		return err
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(text, 10, dst.Type().Bits())
		if err == nil {
			dst.SetUint(n)
		}
		return err
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(text, dst.Type().Bits())
		if err == nil {
			dst.SetFloat(n)
		}
		return err
	case reflect.Bool:
		b, err := strconv.ParseBool(text)
		if err == nil {
			dst.SetBool(b)
		}
		return err
	case reflect.Slice:
		if dst.Type().Elem().Kind() == reflect.Uint8 {
			dst.SetBytes([]byte(text))
			return nil
		}
	}
	if dst.Type() == reflect.TypeOf(time.Time{}) {
		for _, format := range []string{time.RFC3339Nano, "2006-01-02", "15:04:05.999999"} {
			if t, err := time.Parse(format, text); err == nil {
				dst.Set(reflect.ValueOf(t))
				return nil
			}
		}
	}
	return fmt.Errorf("cannot assign %T to %s", value, dst.Type())
}

// ApplyDefaults is explicit and evaluated once for a new instance. Values in
// nonzero Go fields are retained; nullable pointers preserve supplied zero values.
func ApplyDefaults(record Record) error {
	if record.State().DefaultsApplied || record.State().Persisted {
		return nil
	}
	for _, f := range record.Schema().Fields {
		if !f.IsStored() || record.State().Provided[f.Name] {
			continue
		}
		v, err := record.Get(f.Name)
		if err != nil {
			return err
		}
		if !IsEmptyValue(v) {
			continue
		}
		var value any
		if f.DefaultFunc != nil {
			value = f.DefaultFunc()
		} else {
			value = f.Default
		}
		if value != nil {
			if err := record.Set(f.Name, value); err != nil {
				return err
			}
		}
	}
	record.State().DefaultsApplied = true
	return nil
}
func IsEmptyValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	return v.IsZero()
}
