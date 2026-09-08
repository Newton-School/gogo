package orm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type saveTerminalBackend struct {
	db.Backend
	rows db.Rows
	err  error
}

func (*saveTerminalBackend) Alias() string       { return "terminal" }
func (*saveTerminalBackend) Dialect() db.Dialect { return updateDialect{} }
func (b *saveTerminalBackend) Query(context.Context, string, ...any) (db.Rows, error) {
	return b.rows, b.err
}

type saveTerminalRows struct {
	db.Rows
	values                         []any
	rows, nexts, closes            int
	scanErr, terminalErr, closeErr error
	nextHook, closeHook            func()
	panicAt                        string
}

func (r *saveTerminalRows) Next() bool {
	r.nexts++
	if r.panicAt == "next" {
		panic("next panic")
	}
	if r.nexts > 1 && r.nextHook != nil {
		r.nextHook()
	}
	return r.nexts <= r.rows
}
func (r *saveTerminalRows) Scan(dest ...any) error {
	if r.panicAt == "scan" {
		panic("scan panic")
	}
	if r.scanErr != nil {
		return r.scanErr
	}
	for i := range dest {
		*dest[i].(*any) = r.values[i]
	}
	return nil
}
func (r *saveTerminalRows) Err() error {
	if r.panicAt == "err" {
		panic("err panic")
	}
	return r.terminalErr
}
func (r *saveTerminalRows) Close() error {
	r.closes++
	if r.closeHook != nil {
		r.closeHook()
	}
	if r.panicAt == "close" {
		panic("close panic")
	}
	return r.closeErr
}

func TestSaveReturningCompletesBeforeHydrationAndPostHooks(t *testing.T) {
	failure := errors.New("provider failure")
	for _, stage := range []string{"next-error", "close-error", "post-close-error", "scan-error", "partial-query", "nil", "typed-nil", "empty", "extra", "cancel-next", "cancel-close", "success"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rows := &saveTerminalRows{values: []any{int64(99), int64(123)}, rows: 1}
			backend := &saveTerminalBackend{rows: rows}
			want := failure
			switch stage {
			case "next-error":
				rows.nextHook = func() { rows.terminalErr = failure }
			case "close-error":
				rows.closeErr = failure
			case "post-close-error":
				rows.closeHook = func() { rows.terminalErr = failure }
			case "scan-error":
				rows.scanErr = failure
			case "partial-query":
				backend.err = failure
			case "nil":
				backend.rows = nil
				want = nil
			case "typed-nil":
				backend.rows = (*saveTerminalRows)(nil)
				want = nil
			case "extra":
				rows.rows = 3
				want = nil
			case "empty":
				rows.rows = 0
				want = nil
			case "cancel-next":
				rows.nextHook = cancel
				want = context.Canceled
			case "cancel-close":
				rows.closeHook = cancel
				want = context.Canceled
			case "success":
				want = nil
			}
			store := New(backend, nil)
			record, err := models.NewRecord(saveSnapshotSchema())
			if err != nil {
				t.Fatal(err)
			}
			if err := record.Set("value", int64(1)); err != nil {
				t.Fatal(err)
			}
			posts := 0
			store.AfterSave = []SaveReceiver{func(context.Context, SaveEvent) error {
				posts++
				if rows.closes != 1 {
					t.Fatal("post before close")
				}
				return nil
			}}
			err = store.Save(ctx, record, SaveOptions{ForceInsert: true})
			id, _ := record.Get("id")
			value, _ := record.Get("value")
			if stage == "success" {
				if err != nil || id != int64(99) || value != int64(123) || posts != 1 || !record.State().Persisted {
					t.Fatal(err, id, value, posts, record.State())
				}
			} else {
				if err == nil || want != nil && !errors.Is(err, want) || !models.IsEmptyValue(id) || value != int64(1) || posts != 0 || record.State().Persisted {
					t.Fatal(err, id, value, posts, record.State())
				}
			}
			if stage != "nil" && stage != "typed-nil" && rows.closes != 1 {
				t.Fatal("close count", rows.closes)
			}
			if rows.nexts > 2 {
				t.Fatal("unbounded reader", rows.nexts)
			}
			if stage == "empty" && errors.Is(err, db.ErrNoRows) {
				t.Fatal("missing insert row became a lookup miss", err)
			}
		})
	}
}

func TestSaveReturningNoRowsAndPreservedCombinedErrors(t *testing.T) {
	first, terminal, closeFailure := errors.New("scan"), errors.New("terminal"), errors.New("close")
	rows := &saveTerminalRows{rows: 1, scanErr: first, terminalErr: terminal, closeErr: closeFailure}
	backend := &saveTerminalBackend{rows: rows}
	if _, found, err := readSaveReturning(context.Background(), backend, "", nil, 1); found || !errors.Is(err, first) || !errors.Is(err, terminal) || !errors.Is(err, closeFailure) {
		t.Fatal(found, err)
	}
	rows = &saveTerminalRows{}
	backend.rows = rows
	store := New(backend, nil)
	record := saveSnapshotRecord(t)
	if err := store.Save(context.Background(), record, SaveOptions{ForceUpdate: true}); !errors.Is(err, ErrNotUpdated) {
		t.Fatal(err)
	}
	if rows.closes != 1 || record.State().Persisted {
		t.Fatal(rows.closes, record.State())
	}
}

func TestSaveReturningBuffersAndPanicTiming(t *testing.T) {
	buffer := []byte("before")
	rows := &saveTerminalRows{rows: 1, values: []any{buffer}, nextHook: func() { copy(buffer, "after!") }}
	backend := &saveTerminalBackend{rows: rows}
	values, found, err := readSaveReturning(context.Background(), backend, "", nil, 1)
	if err != nil || !found || !reflect.DeepEqual(values, []any{[]byte("before")}) {
		t.Fatal(values, found, err)
	}
	type namedPointer *int
	value := 7
	custom := namedPointer(&value)
	backend.rows = &saveTerminalRows{rows: 1, values: []any{custom}}
	values, found, err = readSaveReturning(context.Background(), backend, "", nil, 1)
	if err != nil || !found || values[0] != custom {
		t.Fatal("custom provider type or identity changed", values, found, err)
	}
	for _, stage := range []string{"next", "scan", "err", "close"} {
		t.Run(stage, func(t *testing.T) {
			rows := &saveTerminalRows{rows: 1, values: []any{int64(99), int64(123)}, panicAt: stage}
			store := New(&saveTerminalBackend{rows: rows}, nil)
			record, err := models.NewRecord(saveSnapshotSchema())
			if err != nil {
				t.Fatal(err)
			}
			posts := 0
			store.AfterSave = []SaveReceiver{func(context.Context, SaveEvent) error { posts++; return nil }}
			defer func() {
				if recovered := recover(); recovered != stage+" panic" {
					t.Fatal("panic changed", recovered)
				}
				id, _ := record.Get("id")
				if !models.IsEmptyValue(id) || record.State().Persisted || posts != 0 || rows.closes != 1 {
					t.Fatal(id, record.State(), posts, rows.closes)
				}
			}()
			_ = store.Save(context.Background(), record, SaveOptions{ForceInsert: true})
			t.Fatal("panic swallowed")
		})
	}
}
