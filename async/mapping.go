package async

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// RegisterMap registers a single queue task that executes the declared handler
// sequentially for a bounded input snapshot. A retried chunk can repeat earlier
// item effects; each item receives a stable ID derived from its chunk identity.
func RegisterMap[I, O any](registry *Registry, name string, version int, task *Task[I, O], options TaskOptions) (*Task[[]I, []O], error) {
	if task == nil || task.definition.call == nil {
		return nil, ErrInvalid
	}
	previous := options.ValidatePayload
	options.ValidatePayload = func(raw json.RawMessage) error {
		if previous != nil {
			if err := previous(raw); err != nil {
				return err
			}
		}
		var items []I
		if err := decodeJSON(raw, &items); err != nil || len(items) > 1000 {
			return ErrInvalid
		}
		for _, item := range items {
			if _, err := task.Signature(item); err != nil {
				return err
			}
		}
		return nil
	}
	return Register(registry, name, version, func(ctx context.Context, tc TaskContext, items []I) ([]O, error) {
		if len(items) > 1000 {
			return nil, ErrInvalid
		}
		output := make([]O, 0, len(items))
		for index, item := range items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			s, err := task.Signature(item)
			if err != nil {
				return nil, err
			}
			child := tc
			child.ID = StableID(tc.ID, fmt.Sprintf("item-%d", index))
			child.ParentID = tc.ID
			if task.definition.options.Authorize != nil {
				if err := task.definition.options.Authorize(ctx, child); err != nil {
					return nil, ErrDenied
				}
			}
			raw, err := task.definition.call(ctx, child, s.Args)
			if err != nil {
				return nil, err
			}
			var value O
			if err := decodeJSON(raw, &value); err != nil {
				return nil, err
			}
			output = append(output, value)
		}
		return output, nil
	}, options)
}

func Chunks[I, O any](mapped *Task[[]I, []O], items []I, size int) (Canvas, error) {
	if mapped == nil || size < 1 || len(items) > 100000 {
		return Canvas{}, ErrInvalid
	}
	signatures := make([]Signature, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		signature, err := mapped.Signature(append([]I(nil), items[start:min(start+size, len(items))]...))
		if err != nil {
			return Canvas{}, err
		}
		signatures = append(signatures, signature)
	}
	if len(signatures) > 1000 {
		return Canvas{}, ErrInvalid
	}
	return Group(signatures...), nil
}

// RegisterStarMap binds each JSON tuple to the exported fields of the declared
// Go argument struct in declaration order, then uses its strict typed codec.
func RegisterStarMap[I, O any](registry *Registry, name string, version int, task *Task[I, O], options TaskOptions) (*Task[[][]json.RawMessage, []O], error) {
	if task == nil || task.definition.call == nil {
		return nil, ErrInvalid
	}
	typ := reflect.TypeFor[I]()
	if typ.Kind() != reflect.Struct {
		return nil, ErrInvalid
	}
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		names = append(names, name)
	}
	previous := options.ValidatePayload
	options.ValidatePayload = func(raw json.RawMessage) error {
		if previous != nil {
			if err := previous(raw); err != nil {
				return err
			}
		}
		var tuples [][]json.RawMessage
		if err := decodeJSON(raw, &tuples); err != nil || len(tuples) > 1000 {
			return ErrInvalid
		}
		for _, tuple := range tuples {
			if len(tuple) != len(names) {
				return ErrInvalid
			}
			values := map[string]json.RawMessage{}
			for i, value := range tuple {
				values[names[i]] = value
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				return ErrInvalid
			}
			if err := task.definition.validate(encoded); err != nil {
				return err
			}
		}
		return nil
	}
	return Register(registry, name, version, func(ctx context.Context, tc TaskContext, tuples [][]json.RawMessage) ([]O, error) {
		if len(tuples) > 1000 {
			return nil, ErrInvalid
		}
		output := make([]O, 0, len(tuples))
		for index, tuple := range tuples {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(tuple) != len(names) {
				return nil, ErrInvalid
			}
			values := map[string]json.RawMessage{}
			for i, value := range tuple {
				values[names[i]] = value
			}
			raw, err := json.Marshal(values)
			if err != nil {
				return nil, ErrInvalid
			}
			if err := task.definition.validate(raw); err != nil {
				return nil, err
			}
			child := tc
			child.ID = StableID(tc.ID, fmt.Sprintf("item-%d", index))
			child.ParentID = tc.ID
			if task.definition.options.Authorize != nil {
				if err := task.definition.options.Authorize(ctx, child); err != nil {
					return nil, ErrDenied
				}
			}
			result, err := task.definition.call(ctx, child, raw)
			if err != nil {
				return nil, err
			}
			var value O
			if err := decodeJSON(result, &value); err != nil {
				return nil, err
			}
			output = append(output, value)
		}
		return output, nil
	}, options)
}
