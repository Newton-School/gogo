package orm

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type iteratorCompletionBackend struct {
	db.Backend
	rows      db.Rows
	err       error
	queryHook func()
	aliasHook func()
}

func (b *iteratorCompletionBackend) Alias() string {
	if b.aliasHook != nil {
		b.aliasHook()
	}
	return "iterator_completion"
}
func (*iteratorCompletionBackend) Dialect() db.Dialect { return updateDialect{} }
func (b *iteratorCompletionBackend) Query(context.Context, string, ...any) (db.Rows, error) {
	if b.queryHook != nil {
		b.queryHook()
	}
	return b.rows, b.err
}

type iteratorCompletionRows struct {
	db.Rows
	values                        [][]any
	nexts, scans, closes          int
	err, closeErr, afterCloseErr  error
	scanErr                       error
	nextHook, scanHook, closeHook func()
	panicAt                       string
	errHook                       func()
}

func (r *iteratorCompletionRows) Next() bool {
	r.nexts++
	if r.nextHook != nil {
		r.nextHook()
	}
	if r.panicAt == "next" {
		panic("iterator next panic")
	}
	return r.nexts <= len(r.values)
}
func (r *iteratorCompletionRows) Scan(dest ...any) error {
	r.scans++
	if r.scanHook != nil {
		r.scanHook()
	}
	if r.panicAt == "scan" {
		panic("iterator scan panic")
	}
	if r.scanErr != nil {
		return r.scanErr
	}
	for index, value := range r.values[r.nexts-1] {
		*dest[index].(*any) = value
	}
	return nil
}
func (r *iteratorCompletionRows) Err() error {
	if r.errHook != nil {
		r.errHook()
	}
	if r.panicAt == "err" {
		panic("iterator err panic")
	}
	if r.closes > 0 {
		return r.afterCloseErr
	}
	return r.err
}
func (r *iteratorCompletionRows) Close() error {
	r.closes++
	if r.closeHook != nil {
		r.closeHook()
	}
	if r.panicAt == "close" {
		panic("iterator close panic")
	}
	return r.closeErr
}

func iteratorCompletionQuery(backend *iteratorCompletionBackend) Query[*models.MapRecord] {
	schema := models.Schema{AppLabel: "iterator", Name: "Item", Fields: []models.Field{
		models.AutoField("id"), models.IntegerField("value"),
	}}
	return For(New(backend, nil), func() *models.MapRecord {
		row, _ := models.NewRecord(schema)
		return row
	})
}

func TestIteratorCompletionPreservesEveryTerminalFailure(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "exhausted"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			before, closing, after := errors.New("before close"), errors.New("close"), errors.New("after close")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rows := &iteratorCompletionRows{err: before, closeErr: closing, afterCloseErr: after, closeHook: cancel}
			iterator, err := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows}).Iterator(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !explicit && iterator.Next() {
				t.Fatal("empty reader emitted a row")
			}
			for range 2 {
				err = iterator.Close()
				for _, failure := range []error{before, closing, after, context.Canceled} {
					if !errors.Is(err, failure) || !errors.Is(iterator.Err(), failure) {
						t.Error("terminal cause lost", failure, err, iterator.Err())
					}
				}
			}
			if rows.closes != 1 || iterator.Next() {
				t.Fatal("iterator did not remain closed", rows.closes)
			}
		})
	}
}

func TestIteratorCompletionAllAndGetDoNotHideTerminalFailure(t *testing.T) {
	failure := errors.New("terminal failure")
	for _, count := range []int{0, 1} {
		for _, postClose := range []bool{false, true} {
			rows := &iteratorCompletionRows{}
			if count == 1 {
				rows.values = [][]any{{int64(7), int64(19)}}
			}
			if postClose {
				rows.afterCloseErr = failure
			} else {
				rows.closeErr = failure
			}
			query := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows})
			values, err := query.All(context.Background())
			// Preserve the existing decoded-prefix contract, but never return
			// that prefix with a nil error after terminal failure.
			if len(values) != count || !errors.Is(err, failure) || rows.closes != 1 {
				t.Fatal("All concealed terminal error or changed prefix", count, postClose, len(values), err, rows.closes)
			}
			rows.nexts, rows.scans, rows.closes = 0, 0, 0
			value, err := query.Get(context.Background())
			if value != nil || !errors.Is(err, failure) || errors.Is(err, ErrNotFound) || rows.closes != 1 {
				t.Fatal("Get changed failure into a row/miss", count, postClose, value, err, rows.closes)
			}
		}
	}
}

func TestIteratorCompletionReadFailureKeepsCleanupCauses(t *testing.T) {
	for _, stage := range []string{"scan", "decode"} {
		t.Run(stage, func(t *testing.T) {
			readFailure, before, closing, after := errors.New("read"), errors.New("before"), errors.New("close"), errors.New("after")
			rows := &iteratorCompletionRows{values: [][]any{{int64(7), int64(19)}}, err: before, closeErr: closing, afterCloseErr: after}
			query := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows})
			if stage == "scan" {
				rows.scanErr = readFailure
			} else {
				query.schema.Fields[1].Codec = iteratorCompletionCodec{decode: func(any) (any, error) { return nil, readFailure }}
			}
			iterator, err := query.Iterator(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if iterator.Next() || iterator.Value() != nil {
				t.Fatal("failed decode emitted a row")
			}
			for _, failure := range []error{readFailure, before, closing, after} {
				if !errors.Is(iterator.Err(), failure) || !errors.Is(iterator.Close(), failure) {
					t.Error("read/cleanup cause lost", failure, iterator.Err())
				}
			}
			if rows.closes != 1 {
				t.Fatal("resource closed more than once", rows.closes)
			}
		})
	}
}

type iteratorCompletionCodec struct{ decode func(any) (any, error) }

func (c iteratorCompletionCodec) Decode(value any) (any, error) { return c.decode(value) }
func (iteratorCompletionCodec) Encode(value any) (any, error)   { return value, nil }

func TestIteratorCompletionConstructorRejectsMissingAndPartialReaders(t *testing.T) {
	for _, kind := range []string{"nil", "typed nil", "partial", "canceled query"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			queryFailure, before, closing, after := errors.New("query"), errors.New("before"), errors.New("close"), errors.New("after")
			rows := &iteratorCompletionRows{err: before, closeErr: closing, afterCloseErr: after}
			backend := &iteratorCompletionBackend{rows: rows}
			switch kind {
			case "nil":
				backend.rows = nil
			case "typed nil":
				backend.rows = (*iteratorCompletionRows)(nil)
			case "partial":
				backend.err = queryFailure
				backend.queryHook = cancel
			case "canceled query":
				backend.queryHook = cancel
			}
			iterator, err := iteratorCompletionQuery(backend).Iterator(ctx)
			if iterator != nil || err == nil || errors.Is(err, ErrNotFound) {
				t.Fatal("invalid provider response was accepted", iterator, err)
			}
			if kind == "partial" || kind == "canceled query" {
				for _, failure := range []error{before, closing, after, context.Canceled} {
					if !errors.Is(err, failure) {
						t.Error("constructor cleanup lost cause", failure, err)
					}
				}
				if kind == "partial" && !errors.Is(err, queryFailure) || rows.closes != 1 || rows.nexts != 0 {
					t.Fatal("partial resource not closed before return", err, rows.closes, rows.nexts)
				}
			}
		})
	}
}

func TestIteratorCompletionCancellationCannotPublishANewRow(t *testing.T) {
	for _, stage := range []string{"entry", "next", "scan", "factory", "decode", "alias"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rows := &iteratorCompletionRows{values: [][]any{{int64(7), int64(19)}}}
			backend := &iteratorCompletionBackend{rows: rows}
			query := iteratorCompletionQuery(backend)
			iterator, err := query.Iterator(ctx)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "entry":
				cancel()
			case "next":
				rows.nextHook = cancel
			case "scan":
				rows.scanHook = cancel
			case "factory":
				factory := iterator.query.factory
				iterator.query.factory = func() *models.MapRecord { cancel(); return factory() }
			case "decode":
				iterator.query.schema.Fields[1].Codec = iteratorCompletionCodec{decode: func(value any) (any, error) { cancel(); return value, nil }}
			case "alias":
				backend.aliasHook = cancel
			}
			if iterator.Next() || iterator.Value() != nil || !errors.Is(iterator.Err(), context.Canceled) || rows.closes != 1 {
				t.Fatal("canceled read published a row", stage, iterator.Value(), iterator.Err(), rows.closes)
			}
			if stage == "entry" && rows.nexts != 0 || stage == "next" && rows.scans != 0 {
				t.Fatal("reader continued after observed cancellation", stage, rows.nexts, rows.scans)
			}
		})
	}
}

type iteratorCompletionContext struct {
	context.Context
	hook func()
}

func (c *iteratorCompletionContext) Err() error {
	if hook := c.hook; hook != nil {
		c.hook = nil
		hook()
	}
	return c.Context.Err()
}

func TestIteratorCompletionReentrantCloseRetainsFailure(t *testing.T) {
	for _, stage := range []string{"next", "scan", "factory", "decode", "context", "close"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("close failure")
			ctx := &iteratorCompletionContext{Context: context.Background()}
			rows := &iteratorCompletionRows{values: [][]any{{int64(7), int64(19)}}, closeErr: failure}
			iterator, err := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows}).Iterator(ctx)
			if err != nil {
				t.Fatal(err)
			}
			closeReader := func() { _ = iterator.Close() }
			switch stage {
			case "next":
				rows.nextHook = closeReader
			case "scan":
				rows.scanHook = closeReader
			case "factory":
				factory := iterator.query.factory
				iterator.query.factory = func() *models.MapRecord { closeReader(); return factory() }
			case "decode":
				iterator.query.schema.Fields[0].Codec = iteratorCompletionCodec{decode: func(value any) (any, error) { closeReader(); return value, nil }}
				iterator.query.schema.Fields[1].Codec = iteratorCompletionCodec{decode: func(any) (any, error) {
					t.Fatal("another decoder ran after closed ownership")
					return nil, nil
				}}
			case "context":
				ctx.hook = closeReader
			case "close":
				rows.closeHook = closeReader
				_ = iterator.Close()
			}
			if iterator.Next() || iterator.Value() != nil || !errors.Is(iterator.Err(), failure) || !errors.Is(iterator.Close(), failure) || rows.closes != 1 {
				t.Fatal("reentrant close failure was lost", stage, iterator.Err(), rows.closes)
			}
		})
	}
}

func TestIteratorCompletionOriginalPanicSurvivesCleanupPanic(t *testing.T) {
	for _, stage := range []string{"next", "scan", "factory", "decode", "context"} {
		t.Run(stage, func(t *testing.T) {
			ctx := &iteratorCompletionContext{Context: context.Background()}
			rows := &iteratorCompletionRows{values: [][]any{{int64(7), int64(19)}}, closeHook: func() { panic("cleanup panic") }}
			iterator, err := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows}).Iterator(ctx)
			if err != nil {
				t.Fatal(err)
			}
			original := "original " + stage + " panic"
			switch stage {
			case "next":
				rows.nextHook = func() { panic(original) }
			case "scan":
				rows.scanHook = func() { panic(original) }
			case "factory":
				iterator.query.factory = func() *models.MapRecord { panic(original) }
			case "decode":
				iterator.query.schema.Fields[0].Codec = iteratorCompletionCodec{decode: func(any) (any, error) { panic(original) }}
			case "context":
				ctx.hook = func() { panic(original) }
			}
			defer func() {
				if recovered := recover(); recovered != original || iterator.Err() == nil || iterator.Close() == nil || rows.closes != 1 {
					t.Fatal("original panic or cleanup ownership changed", recovered, iterator.Err(), rows.closes)
				}
			}()
			iterator.Next()
			t.Fatal("panic swallowed")
		})
	}
}

func TestIteratorCompletionClosePreservesReturnedErrorsBeforeLaterPanic(t *testing.T) {
	closing := errors.New("returned close failure")
	rows := &iteratorCompletionRows{closeErr: closing}
	rows.errHook = func() {
		if rows.closes > 0 {
			panic("post-close panic")
		}
	}
	iterator, err := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows}).Iterator(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recovered := recover(); recovered != "post-close panic" || !errors.Is(iterator.Err(), closing) || !errors.Is(iterator.Close(), closing) || rows.closes != 1 {
			t.Fatal("returned error lost to later panic", recovered, iterator.Err(), rows.closes)
		}
	}()
	iterator.Close()
	t.Fatal("panic swallowed")
}

func TestIteratorCompletionSuccessAndPanicOwnership(t *testing.T) {
	rows := &iteratorCompletionRows{values: [][]any{{int64(7), int64(19)}}}
	query := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows})
	iterator, err := query.Iterator(context.Background())
	if err != nil || !iterator.Next() {
		t.Fatal("valid row missing", err)
	}
	value := iterator.Value()
	if iterator.Next() || iterator.Err() != nil || iterator.Close() != nil || iterator.Value() != value || rows.closes != 1 {
		t.Fatal("successful completion changed", iterator.Err(), rows.closes)
	}
	for _, stage := range []string{"next", "scan", "err", "close"} {
		t.Run(stage, func(t *testing.T) {
			rows := &iteratorCompletionRows{values: [][]any{{int64(7), int64(19)}}, panicAt: stage}
			iterator, err := iteratorCompletionQuery(&iteratorCompletionBackend{rows: rows}).Iterator(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if recovered := recover(); recovered != "iterator "+stage+" panic" || rows.closes != 1 || iterator.Err() == nil || iterator.Close() == nil {
					t.Fatal("panic ownership changed", recovered, rows.closes)
				}
			}()
			if stage == "next" || stage == "scan" {
				iterator.Next()
			} else {
				iterator.Close()
			}
			t.Fatal("provider panic was swallowed")
		})
	}
}

type iteratorCompletionModel struct {
	*models.MapRecord
	schemaHook func()
}

func (m *iteratorCompletionModel) Schema() models.Schema {
	if m.schemaHook != nil {
		m.schemaHook()
	}
	return m.MapRecord.Schema()
}

func TestIteratorCompletionFactoryClosePreventsBindingCallbacks(t *testing.T) {
	rows := &iteratorCompletionRows{values: [][]any{{int64(7), int64(19)}}}
	backend := &iteratorCompletionBackend{rows: rows}
	base := iteratorCompletionQuery(backend)
	var iterator *Iterator[*iteratorCompletionModel]
	binds := 0
	query := For(base.store, func() *iteratorCompletionModel {
		model := &iteratorCompletionModel{MapRecord: base.factory()}
		if iterator != nil {
			_ = iterator.Close()
			model.schemaHook = func() { binds++ }
		}
		return model
	})
	var err error
	iterator, err = query.Iterator(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if iterator.Next() || binds != 0 || rows.scans != 0 || rows.closes != 1 {
		t.Fatal("binding callbacks continued after factory closed reader", binds, rows.scans, rows.closes)
	}
}
