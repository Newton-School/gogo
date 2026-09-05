package models

import "fmt"

// MapRecord is a schema-bound data snapshot used by generic tooling and graph
// collectors. It does not invent a Go struct or run typed model hooks.
type MapRecord struct {
	schema Schema
	state  State
	values map[string]any
}

func NewRecord(schema Schema) (*MapRecord, error) {
	if err := schema.Validate(); err != nil {
		return nil, err
	}
	values := map[string]any{}
	for _, field := range schema.Fields {
		if field.IsStored() {
			values[field.Name] = nil
		}
	}
	return &MapRecord{schema: schema.Clone(), values: values}, nil
}
func (r *MapRecord) Schema() Schema     { return r.schema.Clone() }
func (r *MapRecord) State() *State      { return &r.state }
func (r *MapRecord) ModelState() *State { return &r.state }
func (r *MapRecord) Model() Model       { return r }
func (r *MapRecord) Get(name string) (any, error) {
	if _, ok := r.schema.Field(name); !ok {
		return nil, fmt.Errorf("models: unknown field %s", name)
	}
	value, ok := r.values[name]
	if !ok {
		return nil, fmt.Errorf("models: field %s has not been loaded", name)
	}
	return value, nil
}
func (r *MapRecord) Set(name string, value any) error {
	if _, ok := r.schema.Field(name); !ok {
		return fmt.Errorf("models: unknown field %s", name)
	}
	r.values[name] = value
	if r.state.Provided == nil {
		r.state.Provided = map[string]bool{}
	}
	r.state.Provided[name] = true
	return nil
}
