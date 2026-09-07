package orm

import (
	"context"
	"errors"
	"reflect"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// ErrDeleteCallbackMutation rejects a changed authorization or hook view and
// rolls back the entire deletion transaction.
var ErrDeleteCallbackMutation = errors.New("orm: deletion callback changed its read-only view")

// ErrDeleteCallbackView means a bounded, safely detached callback snapshot
// cannot be constructed. No callback or deletion write has run at that point.
var ErrDeleteCallbackView = errors.New("orm: deletion callback value cannot be safely detached")

// A deletion view has no reference back to its execution record. Schema returns
// fresh descriptor data; registered functions/codecs remain trusted runtime
// services, not application data to execute or reflectively copy.
type deleteRecordView struct {
	schema models.Schema
	state  models.State
	values map[string]any
}

func (r *deleteRecordView) Schema() models.Schema {
	copy, err := (&deleteClone{}).schema(r.schema)
	if err != nil {
		panic(ErrDeleteCallbackView) // Initial view creation already validated it.
	}
	return copy
}
func (r *deleteRecordView) State() *models.State { return &r.state }
func (r *deleteRecordView) Get(name string) (any, error) {
	if _, ok := r.schema.Field(name); !ok || r.state.Deferred[name] {
		return nil, ErrDeleteCallbackView
	}
	value, ok := r.values[name]
	if !ok {
		return nil, ErrDeleteCallbackView
	}
	return value, nil
}
func (r *deleteRecordView) Set(name string, value any) error {
	if _, ok := r.schema.Field(name); !ok {
		return ErrDeleteCallbackView
	}
	r.values[name] = value
	return nil
}

type deleteViewGuard struct {
	view   *deleteRecordView
	state  models.State
	values map[string]any
	clone  *deleteClone
}

func guardDeleteRecord(view *deleteRecordView) (deleteViewGuard, error) {
	clone := &deleteClone{}
	state, err := clone.value(view.state)
	if err != nil {
		return deleteViewGuard{}, err
	}
	values, err := clone.value(view.values)
	if err != nil {
		return deleteViewGuard{}, err
	}
	guard := deleteViewGuard{view, state.(models.State), values.(map[string]any), clone}
	if !guard.unchanged() {
		return deleteViewGuard{}, ErrDeleteCallbackView
	}
	return guard, nil
}
func (g deleteViewGuard) unchanged() bool {
	nodes, bytes := g.clone.nodes, g.clone.bytes
	defer func() { g.clone.nodes, g.clone.bytes = nodes, bytes }()
	equal := deleteEquality{clone: g.clone}
	return equal.value(reflect.ValueOf(g.view.state), reflect.ValueOf(g.state), 0) && equal.value(reflect.ValueOf(g.view.values), reflect.ValueOf(g.values), 0)
}

func deleteCallback(ctx context.Context, record models.Record, receiver DeleteReceiver) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	view, err := (&deleteClone{}).record(record)
	if err != nil {
		return err
	}
	guard, err := guardDeleteRecord(view)
	if err != nil {
		return err
	}
	if err := receiver(ctx, DeleteEvent{Record: view}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !guard.unchanged() {
		return ErrDeleteCallbackMutation
	}
	return nil
}

func authorizeDelete(ctx context.Context, canonical DeletionPlan, authorize func(context.Context, DeletionPlan) error) error {
	clone := &deleteClone{}
	view := DeletionPlan{Objects: make([]models.Record, len(canonical.Objects)), Updates: make([]FieldUpdate, len(canonical.Updates)), JoinRemovals: make([]JoinRemoval, len(canonical.JoinRemovals))}
	for i, record := range canonical.Objects {
		item, err := clone.record(record)
		if err != nil {
			return err
		}
		view.Objects[i] = item
	}
	for i, update := range canonical.Updates {
		record, err := clone.record(update.Record)
		if err != nil {
			return err
		}
		value, err := clone.value(update.Value)
		if err != nil {
			return err
		}
		view.Updates[i] = FieldUpdate{record, update.Field, value}
	}
	for i, removal := range canonical.JoinRemovals {
		record, err := clone.record(removal.Record)
		if err != nil {
			return err
		}
		endpoint, err := clone.record(removal.Endpoint)
		if err != nil {
			return err
		}
		view.JoinRemovals[i] = JoinRemoval{record, endpoint, removal.Field}
	}
	// Protected graphs return before authorization, so there are no protected
	// records here. Detached expected descriptors detect slice substitution.
	objects := append([]models.Record(nil), view.Objects...)
	updates := append([]FieldUpdate(nil), view.Updates...)
	removals := append([]JoinRemoval(nil), view.JoinRemovals...)
	updateClones := make([]*deleteClone, len(updates))
	for i := range updates {
		updateClones[i] = &deleteClone{}
		value, err := updateClones[i].value(updates[i].Value)
		if err != nil {
			return err
		}
		updates[i].Value = value
		if !equalDeleteValue(updateClones[i], view.Updates[i].Value, value) {
			return ErrDeleteCallbackView
		}
	}
	guards := make([]deleteViewGuard, 0, len(clone.records))
	for _, record := range clone.records {
		guard, err := guardDeleteRecord(record)
		if err != nil {
			return err
		}
		guards = append(guards, guard)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, view); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, original := range objects {
		if !sameDeleteView(view.Objects[i], original) {
			return ErrDeleteCallbackMutation
		}
	}
	for i, original := range updates {
		if !sameDeleteView(view.Updates[i].Record, original.Record) || view.Updates[i].Field != original.Field || !equalDeleteValue(updateClones[i], view.Updates[i].Value, original.Value) {
			return ErrDeleteCallbackMutation
		}
	}
	for i, original := range removals {
		if !sameDeleteView(view.JoinRemovals[i].Record, original.Record) || !sameDeleteView(view.JoinRemovals[i].Endpoint, original.Endpoint) || view.JoinRemovals[i].Field != original.Field {
			return ErrDeleteCallbackMutation
		}
	}
	for _, guard := range guards {
		if !guard.unchanged() {
			return ErrDeleteCallbackMutation
		}
	}
	return nil
}

func sameDeleteView(value, expected models.Record) bool {
	view, ok := value.(*deleteRecordView)
	return ok && view == expected.(*deleteRecordView)
}

// A scope may decide predicates, but cannot rewrite the compiler's schema or
// mutate declarative defaults subsequently used by the collected graph.
func (c DeleteCollector) deletionScope(ctx context.Context, schema models.Schema) (db.Predicate, error) {
	view, err := (&deleteClone{}).schema(schema)
	if err != nil {
		return db.Predicate{}, err
	}
	return c.Scope(ctx, view)
}
