package admin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
)

// The record and its selected values are captured before save callbacks. Saved
// identity is attached only after the scoped store returns the persisted row.
type toOneWrite struct {
	state  *toOneSelectState
	object Object
	values map[string]toOneSnapshot
}

type toOneTargetFence struct {
	scope     ScopedStore
	field     models.Field
	reference toOneReference
}

type toOnePreparedFence struct {
	source    ScopedStore
	model, id string
	values    map[string]toOneSnapshot
	targets   []toOneTargetFence
}

// All parent/inline authorization callbacks finish before any final read. All
// source/target scopes are then prepared before any final read, too. Running a
// complete fence per row would let a later row callback invalidate earlier rows.
func finishToOneWrites(ctx context.Context, writes []toOneWrite) error {
	for _, write := range writes {
		if err := write.state.recheck(ctx, write.values); err != nil {
			return err
		}
	}
	prepared := make([]*toOnePreparedFence, 0, len(writes))
	for _, write := range writes {
		fence, err := write.prepare(ctx)
		if err != nil {
			return err
		}
		if fence != nil {
			prepared = append(prepared, fence)
		}
	}
	for _, fence := range prepared {
		if err := fence.check(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (write toOneWrite) prepare(ctx context.Context) (*toOnePreparedFence, error) {
	if len(write.values) == 0 {
		return nil, nil
	}
	state := write.state
	scope, err := state.site.config.Store.Scope(ctx, state.principal, state.site.config.Name, state.options.Schema)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		return nil, auth.ErrPermissionDenied
	}
	fence := &toOnePreparedFence{source: scope, model: state.options.Schema.Key(), id: write.object.ID, values: write.values}
	for _, name := range state.names {
		if write.values[name].Empty {
			continue
		}
		field, _ := state.options.Schema.Field(name)
		reference, checked := state.checked[name]
		if !checked || reference.ChoiceID != write.values[name].ID {
			return nil, auth.ErrPermissionDenied
		}
		target := state.site.models[field.Relation.Target]
		scope, err := state.site.config.Store.Scope(ctx, state.principal, state.site.config.Name, target.Schema)
		if err != nil {
			return nil, err
		}
		if scope == nil {
			return nil, auth.ErrPermissionDenied
		}
		fence.targets = append(fence.targets, toOneTargetFence{scope: scope, field: field, reference: reference})
	}
	return fence, nil
}

func (fence *toOnePreparedFence) check(ctx context.Context) error {
	current, err := fence.source.Get(ctx, fence.id, true)
	if err != nil {
		return err
	}
	if current.ID != fence.id || current.Record == nil || current.Record.Schema().Key() != fence.model {
		return auth.ErrPermissionDenied
	}
	names := make([]string, 0, len(fence.values))
	for name := range fence.values {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		value, err := current.Record.Get(name)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != fence.values[name].Encoded {
			return auth.ErrPermissionDenied
		}
	}
	for _, target := range fence.targets {
		current, err := target.scope.Get(ctx, target.reference.ObjectID, true)
		if errors.Is(err, ErrNotFound) {
			return auth.ErrPermissionDenied
		}
		if err != nil {
			return err
		}
		if current.ID != target.reference.ObjectID || current.Record == nil || current.Record.Schema().Key() != target.field.Relation.Target {
			return auth.ErrPermissionDenied
		}
		id, err := relationChoiceID(target.field, current)
		if err != nil || id != target.reference.ChoiceID {
			return auth.ErrPermissionDenied
		}
		version, err := objectFromRecord(current.Record)
		if err != nil {
			return err
		}
		if version.Version != target.reference.Version {
			return auth.ErrPermissionDenied
		}
	}
	return ctx.Err()
}
