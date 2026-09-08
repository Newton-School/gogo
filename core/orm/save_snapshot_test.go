package orm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type saveSnapshotBackend struct {
	db.Backend
	alias                                        string
	queries, durable, begins, commits, rollbacks int
	args                                         []any
}

func (b *saveSnapshotBackend) Alias() string       { return b.alias }
func (b *saveSnapshotBackend) Dialect() db.Dialect { return updateDialect{} }
func (b *saveSnapshotBackend) Query(_ context.Context, _ string, args ...any) (db.Rows, error) {
	b.queries++
	b.durable++
	b.args = append([]any(nil), args...)
	return &saveSnapshotRows{values: []any{args[len(args)-1], args[0]}}, nil
}
func (b *saveSnapshotBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.begins++
	return &saveSnapshotTransaction{backend: b}, nil
}

type saveSnapshotTransaction struct {
	db.Transaction
	backend *saveSnapshotBackend
	pending int
}

func (tx *saveSnapshotTransaction) Query(_ context.Context, _ string, args ...any) (db.Rows, error) {
	tx.backend.queries++
	tx.pending++
	tx.backend.args = append([]any(nil), args...)
	return &saveSnapshotRows{values: []any{args[len(args)-1], args[0]}}, nil
}
func (tx *saveSnapshotTransaction) Commit() error {
	tx.backend.commits++
	tx.backend.durable += tx.pending
	return nil
}
func (tx *saveSnapshotTransaction) Rollback() error { tx.backend.rollbacks++; return nil }

type saveSnapshotRows struct {
	values []any
	read   bool
}

func (r *saveSnapshotRows) Columns() ([]string, error) { return []string{"id", "value"}, nil }
func (r *saveSnapshotRows) Next() bool {
	if r.read {
		return false
	}
	r.read = true
	return true
}
func (r *saveSnapshotRows) Scan(dest ...any) error {
	for i := range dest {
		*dest[i].(*any) = r.values[i]
	}
	return nil
}
func (*saveSnapshotRows) Err() error   { return nil }
func (*saveSnapshotRows) Close() error { return nil }

func saveSnapshotSchema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "SaveSnapshot", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.BigIntegerField("value", models.WithStructField("Value"))}}
}
func saveSnapshotRecord(t *testing.T) *models.MapRecord {
	t.Helper()
	record, err := models.NewRecord(saveSnapshotSchema())
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Set("id", int64(1)); err != nil {
		t.Fatal(err)
	}
	if err := record.Set("value", int64(1)); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestSaveSnapshotsBothHookListsBeforeFirstReceiver(t *testing.T) {
	b := &saveSnapshotBackend{alias: "original"}
	store := New(b, nil)
	record := saveSnapshotRecord(t)
	calls := []string{}
	injected := func(context.Context, SaveEvent) error { calls = append(calls, "injected"); return nil }
	before := make([]SaveReceiver, 2, 4)
	after := make([]SaveReceiver, 2, 4)
	before[0] = func(_ context.Context, event SaveEvent) error {
		calls = append(calls, "before1")
		before[1] = injected
		after[1] = injected
		store.BeforeSave = []SaveReceiver{func(context.Context, SaveEvent) error { calls = append(calls, "next_before"); return nil }}
		store.AfterSave = []SaveReceiver{func(context.Context, SaveEvent) error { calls = append(calls, "next_after"); return nil }}
		event.UpdateFields[0] = "event-only"
		return event.Record.Set("value", int64(2))
	}
	before[1] = func(_ context.Context, event SaveEvent) error {
		calls = append(calls, "before2")
		if event.UpdateFields[0] != "event-only" {
			t.Fatal("SaveEvent stopped being mutable")
		}
		return nil
	}
	after[0] = func(_ context.Context, event SaveEvent) error {
		calls = append(calls, "after1")
		after[1] = injected
		if !event.Record.State().Persisted || event.Created || b.durable != 1 {
			t.Fatal("post-save timing changed")
		}
		return nil
	}
	after[1] = func(context.Context, SaveEvent) error { calls = append(calls, "after2"); return nil }
	store.BeforeSave, store.AfterSave = before, after
	options := SaveOptions{UpdateFields: []string{"value"}, Prepare: func(_ context.Context, record models.Record) error {
		calls = append(calls, "prepare")
		return record.Set("value", int64(3))
	}, Guard: func(_ context.Context, record models.Record) error {
		calls = append(calls, "guard")
		value, _ := record.Get("value")
		if value != int64(3) || b.queries != 0 {
			t.Fatal("pre-write guard or preparation changed")
		}
		return nil
	}}
	if err := store.Save(context.Background(), record, options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"before1", "before2", "prepare", "guard", "after1", "after2"}) || !reflect.DeepEqual(b.args, []any{int64(3), int64(1)}) || options.UpdateFields[0] != "value" {
		t.Fatal(calls, b.args, options.UpdateFields)
	}
	calls = nil
	if err := store.Save(context.Background(), record, SaveOptions{ForceUpdate: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"next_before", "next_after"}) {
		t.Fatal("later operation missed registration changes", calls)
	}
}

type saveSnapshotModel struct {
	models.Base
	ID, Value         int64
	onState, onSchema func()
}

func (r *saveSnapshotModel) ModelState() *models.State {
	if r.onState != nil {
		r.onState()
	}
	return r.Base.ModelState()
}
func (r *saveSnapshotModel) Schema() models.Schema {
	if r.onSchema != nil {
		r.onSchema()
	}
	return saveSnapshotSchema()
}

type saveSnapshotContext struct {
	context.Context
	once func()
}

func (c *saveSnapshotContext) Err() error {
	if c.once != nil {
		callback := c.once
		c.once = nil
		callback()
	}
	return c.Context.Err()
}

func TestSaveSnapshotsStoreBeforeContextModelAndPreparationCallbacks(t *testing.T) {
	for _, phase := range []string{"context", "model_state", "model_schema", "before", "prepare", "guard", "after"} {
		t.Run(phase, func(t *testing.T) {
			original := &saveSnapshotBackend{alias: "original"}
			replacement := &saveSnapshotBackend{alias: "replacement"}
			store := New(original, nil)
			calls := []string{}
			changed := false
			change := func() {
				if changed {
					return
				}
				changed = true
				*store = Store{Backend: replacement, BeforeSave: []SaveReceiver{func(context.Context, SaveEvent) error { calls = append(calls, "new_before"); return nil }}, AfterSave: []SaveReceiver{func(context.Context, SaveEvent) error { calls = append(calls, "new_after"); return nil }}}
			}
			store.BeforeSave = []SaveReceiver{func(context.Context, SaveEvent) error {
				calls = append(calls, "old_before")
				if phase == "before" {
					change()
				}
				return nil
			}}
			store.AfterSave = []SaveReceiver{func(context.Context, SaveEvent) error {
				calls = append(calls, "old_after")
				if phase == "after" {
					change()
				}
				return nil
			}}
			model := &saveSnapshotModel{ID: 1, Value: 7}
			var ctx context.Context = context.Background()
			switch phase {
			case "context":
				ctx = &saveSnapshotContext{Context: ctx, once: change}
			case "model_state":
				model.onState = change
			case "model_schema":
				model.onSchema = change
			}
			options := SaveOptions{ForceUpdate: true}
			if phase == "prepare" {
				options.Prepare = func(context.Context, models.Record) error { change(); return nil }
			}
			if phase == "guard" {
				options.Guard = func(context.Context, models.Record) error { change(); return nil }
			}
			if err := store.Save(ctx, model, options); err != nil {
				t.Fatal(err)
			}
			if !changed || original.queries != 1 || replacement.queries != 0 || model.Base.ModelState().Database != "original" || !reflect.DeepEqual(calls, []string{"old_before", "old_after"}) {
				t.Fatal("operation configuration retargeted", phase, original.queries, replacement.queries, calls, model.Base.ModelState().Database)
			}
			calls = nil
			if err := store.Save(context.Background(), model, SaveOptions{ForceUpdate: true}); err != nil {
				t.Fatal(err)
			}
			if replacement.queries != 1 || model.Base.ModelState().Database != "replacement" || !reflect.DeepEqual(calls, []string{"new_before", "new_after"}) {
				t.Fatal("next operation did not use new configuration", calls)
			}
		})
	}
}

func TestSaveHookSnapshotKeepsNoopAndErrorTiming(t *testing.T) {
	b := &saveSnapshotBackend{alias: "test"}
	store := New(b, nil)
	record := saveSnapshotRecord(t)
	calls := 0
	denied := errors.New("save hook denied")
	store.BeforeSave = []SaveReceiver{func(context.Context, SaveEvent) error { calls++; return denied }}
	store.AfterSave = []SaveReceiver{func(context.Context, SaveEvent) error { calls++; return denied }}
	if err := store.Save(context.Background(), record, SaveOptions{UpdateFields: []string{}, Prepare: func(context.Context, models.Record) error { calls++; return denied }, Guard: func(context.Context, models.Record) error { calls++; return denied }}); err != nil || calls != 0 || b.queries != 0 || record.State().Persisted {
		t.Fatal("empty update fields stopped being a no-op", err, calls)
	}
	if err := store.Save(context.Background(), record, SaveOptions{ForceUpdate: true}); !errors.Is(err, denied) || calls != 1 || b.queries != 0 {
		t.Fatal("before-save error timing", err, calls)
	}
	store.BeforeSave = nil
	if err := store.Save(context.Background(), record, SaveOptions{ForceUpdate: true}); !errors.Is(err, denied) || b.durable != 1 || !record.State().Persisted {
		t.Fatal("autocommit after-save error timing", err, b.durable)
	}
}

func TestSaveSnapshotsUpdateFieldsBeforeContextCallback(t *testing.T) {
	b := &saveSnapshotBackend{alias: "original"}
	store := New(b, nil)
	record := saveSnapshotRecord(t)
	fields := []string{"value"}
	ctx := &saveSnapshotContext{Context: context.Background(), once: func() { fields[0] = "id" }}
	if err := store.Save(ctx, record, SaveOptions{UpdateFields: fields}); err != nil || b.queries != 1 || !reflect.DeepEqual(b.args, []any{int64(1), int64(1)}) || fields[0] != "id" {
		t.Fatal("context retargeted selected fields", err, b.args, fields)
	}
}

func TestSaveSnapshotsRegistryAndPreservesOwnedCommitBeforePostPanic(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			b := &saveSnapshotBackend{alias: "original"}
			replacement := &saveSnapshotBackend{alias: "replacement"}
			parent := saveSnapshotSchema()
			parent.Name = "SnapshotParent"
			child := saveSnapshotSchema()
			child.Name = "SnapshotChild"
			child.Parent = parent.Key()
			registry := &models.Registry{}
			if err := registry.Register(parent); err != nil {
				t.Fatal(err)
			}
			record, err := models.NewRecord(child)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.Set("id", int64(1)); err != nil {
				t.Fatal(err)
			}
			if err := record.Set("value", int64(2)); err != nil {
				t.Fatal(err)
			}
			store := New(b, registry)
			sentinel := &struct{}{}
			store.BeforeSave = []SaveReceiver{func(context.Context, SaveEvent) error {
				*store = Store{Backend: replacement}
				if phase == "before" {
					panic(sentinel)
				}
				return nil
			}}
			store.AfterSave = []SaveReceiver{func(context.Context, SaveEvent) error { panic(sentinel) }}
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				if err := store.Save(context.Background(), record, SaveOptions{ForceUpdate: true}); err != nil {
					t.Fatal(err)
				}
			}()
			if recovered != sentinel || replacement.queries != 0 || replacement.begins != 0 {
				t.Fatal("owned save retargeted or panic changed", recovered)
			}
			if phase == "before" {
				if b.queries != 0 || b.begins != 0 || record.State().Persisted {
					t.Fatal("pre-save panic started owned transaction")
				}
			} else if b.queries != 2 || b.begins != 1 || b.commits != 1 || b.durable != 2 || b.rollbacks != 0 || !record.State().Persisted || record.State().Database != "original" {
				t.Fatal("post-save panic changed owned commit boundary", b.queries, b.begins, b.commits, b.durable, b.rollbacks, record.State())
			}
		})
	}
}

func TestSaveHookSnapshotPreservesPanicsAndTransactionOutcomes(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		for _, outer := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "_autocommit", true: "_caller_transaction"}[outer], func(t *testing.T) {
				b := &saveSnapshotBackend{alias: "test"}
				store := New(b, nil)
				record := saveSnapshotRecord(t)
				sentinel := &struct{ phase string }{phase}
				later := false
				hooks := []SaveReceiver{func(context.Context, SaveEvent) error { panic(sentinel) }, func(context.Context, SaveEvent) error { later = true; return nil }}
				if phase == "before" {
					store.BeforeSave = hooks
				} else {
					store.AfterSave = hooks
				}
				var recovered any
				func() {
					defer func() { recovered = recover() }()
					save := func(ctx context.Context) error { return store.Save(ctx, record, SaveOptions{ForceUpdate: true}) }
					if outer {
						_ = db.Atomic(context.Background(), b, db.AtomicOptions{}, save)
					} else {
						_ = save(context.Background())
					}
				}()
				if recovered != sentinel || later {
					t.Fatal("panic was mapped, lost or dispatch continued", recovered, later)
				}
				wantQueries := 0
				if phase == "after" {
					wantQueries = 1
				}
				wantDurable := wantQueries
				if outer {
					wantDurable = 0
				}
				if b.queries != wantQueries || b.durable != wantDurable || b.commits != 0 || outer && b.rollbacks != 1 || !outer && b.rollbacks != 0 {
					t.Fatal("panic changed persistence boundary", b.queries, b.durable, b.commits, b.rollbacks)
				}
			})
		}
	}
}
