package admin

import (
	"context"
	"slices"
)

func inlineSelectionOptions(config Inline) ModelAdmin {
	return ModelAdmin{Schema: config.Schema, Fields: config.Fields, Exclude: config.Exclude, ReadonlyFields: config.Readonly, FormOverrides: config.FormOverrides, ResolveRelation: config.ResolveRelation}
}

// Display choices are loaded once per inline configuration. Each row gets
// separate checked identities, failure state and writable-field selection.
func inlineSelectionRow(base *toOneSelectState, readonly []string) *toOneSelectState {
	row := *base
	row.names = nil
	for _, name := range base.names {
		if !slices.Contains(readonly, name) {
			row.names = append(row.names, name)
		}
	}
	row.checked = map[string]toOneReference{}
	row.failure = nil
	return &row
}

func inlineToOneWrites(states []*inlineState) []toOneWrite {
	var writes []toOneWrite
	for _, state := range states {
		writes = append(writes, state.relationWrites...)
	}
	return writes
}

// Freeze every inline parent link before the first child callback can run.
func prepareInlineParentLinks(parent Object, states []*inlineState) error {
	for _, state := range states {
		field := parent.Record.Schema().PKFields()[0].Name
		fk, _ := state.config.Schema.Field(state.config.FKName)
		if len(fk.Relation.TargetFields) > 0 {
			field = fk.Relation.TargetFields[0]
		}
		values, err := snapshotToOneFields(parent.Record, []string{field})
		if err != nil {
			return err
		}
		state.parentSnapshot = values[field]
		state.parentValue, err = parent.Record.Get(field)
		if err != nil {
			return err
		}
	}
	return nil
}

func prepareInlineRelationWrite(ctx context.Context, state *inlineState, index int, object Object) (toOneWrite, error) {
	selected := state.selections[index]
	values := state.selected[index]
	if values == nil {
		values = map[string]toOneSnapshot{}
	}
	if err := selected.recheck(ctx, values); err != nil {
		return toOneWrite{}, err
	}
	// Parent ownership is server-controlled, regardless of editable relation
	// choices or custom widgets. The final source read verifies this identity.
	values[state.config.FKName] = state.parentSnapshot
	return toOneWrite{state: selected, object: object, values: values}, nil
}
