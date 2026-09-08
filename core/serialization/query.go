package serialization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"io"
	"time"
)

func query(ctx context.Context, store *orm.Store, p profile) (orm.Query[*models.MapRecord], error) {
	factory := func() *models.MapRecord { r, _ := models.NewRecord(p.schema); return r }
	q := orm.For(store, factory).OrderBy(p.pk...)
	if e := contextError(ctx); e != nil {
		return q, e
	}
	predicate, e := p.scope(ctx, p.schema.Clone())
	predicate = orm.SnapshotPredicate(predicate)
	if e != nil {
		return q, ErrUnavailable
	}
	if e = contextError(ctx); e != nil {
		return q, e
	}
	return q.Filter(predicate), nil
}

func cloneRecord(p profile, r Record) (Record, error) {
	out := Record{Model: r.Model, PK: map[string]any{}, Fields: map[string]any{}}
	for _, f := range p.schema.Fields {
		v := r.Fields[f.Name]
		if isPK(p, f.Name) {
			v = r.PK[f.Name]
		}
		d, _, e := canonical(f, v, isPK(p, f.Name))
		if e != nil {
			return Record{}, e
		}
		if isPK(p, f.Name) {
			out.PK[f.Name] = d
		} else {
			out.Fields[f.Name] = d
		}
	}
	return out, nil
}

func detachRaw(v any, budget *valueBudget) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch x := v.(type) {
	case []byte:
		if len(x) > budget.text || budget.nodes <= 0 {
			return nil, ErrLimit
		}
		budget.text -= len(x)
		budget.nodes--
		return append([]byte(nil), x...), nil
	case time.Time:
		budget.nodes--
		if budget.nodes < 0 {
			return nil, ErrLimit
		}
		// Local initializes lazily through the original descriptor's identity.
		// Resolve its standard-library zone before copying that descriptor.
		_, _ = x.Zone()
		z := *x.Location()
		return x.In(&z), nil
	case time.Duration:
		budget.nodes--
		if budget.nodes < 0 {
			return nil, ErrLimit
		}
		return x, nil
	}
	return budget.json(v, 0)
}
func decodeRaw(f models.Field, v any, dialect db.Dialect) (any, error) {
	if v == nil {
		return nil, nil
	}
	if f.Kind == models.JSON {
		var raw []byte
		switch x := v.(type) {
		case string:
			raw = []byte(x)
		case []byte:
			raw = x
		}
		if raw != nil {
			if !validJSONText(raw) {
				return nil, ErrInvalid
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			nodes := 65536
			n, e := strictValue(decoder, 0, &nodes)
			if e != nil {
				return nil, e
			}
			if _, e := decoder.Token(); e != io.EOF {
				return nil, ErrInvalid
			}
			if n == nil {
				return models.JSONNull, nil
			}
			return n, nil
		}
		return v, nil
	}
	if f.Kind == models.Duration {
		if decoder, ok := dialect.(db.FieldValueDecoder); ok {
			return decoder.DecodeFieldValue(f, v)
		}
	}
	return v, nil
}

// readRows fully checks the bounded page's terminal outcome before publishing
// it. Cells are detached immediately after Scan, before another provider or
// context callback can alter a buffer or retained timestamp Location.
func readRows(ctx context.Context, executor db.Executor, dialect db.Dialect, p profile, q orm.Query[*models.MapRecord], limit int) (out []Record, err error) {
	statement, args, e := q.Limit(limit).SQLContext(ctx)
	if e != nil {
		return nil, ErrUnavailable
	}
	if e = contextError(ctx); e != nil {
		return nil, e
	}
	rows, e := executor.Query(ctx, statement, args...)
	if nilValue(rows) {
		return nil, ErrUnavailable
	}
	closed := false
	closeRows := func() error {
		if closed {
			return nil
		}
		closed = true
		return errors.Join(safeCall(rows.Err), safeCall(rows.Close), safeCall(rows.Err))
	}
	defer func() {
		panicValue := recover()
		terminal := closeRows()
		if panicValue != nil {
			err = ErrUnavailable
		}
		if terminal != nil {
			err = ErrUnavailable
		}
		if err != nil {
			out = nil
		}
	}()
	if e != nil {
		return nil, ErrUnavailable
	}
	for rows.Next() {
		if len(out) >= limit {
			return nil, ErrUnavailable
		}
		values := make([]any, len(p.schema.Fields))
		dest := make([]any, len(values))
		for i := range dest {
			dest[i] = &values[i]
		}
		if rows.Scan(dest...) != nil {
			return nil, ErrUnavailable
		}
		budget := valueBudget{nodes: 65536, text: MaxRecordBytes}
		for i, v := range values {
			values[i], e = detachRaw(v, &budget)
			if e != nil {
				return nil, e
			}
		}
		r := Record{Model: p.schema.Key(), PK: map[string]any{}, Fields: map[string]any{}}
		for i, f := range p.schema.Fields {
			v, e := decodeRaw(f, values[i], dialect)
			if e != nil {
				return nil, ErrUnavailable
			}
			d, _, e := canonical(f, v, isPK(p, f.Name))
			if e != nil {
				return nil, ErrUnavailable
			}
			if isPK(p, f.Name) {
				r.PK[f.Name] = d
			} else {
				r.Fields[f.Name] = d
			}
		}
		if e = contextError(ctx); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	if e = closeRows(); e != nil {
		return nil, ErrUnavailable
	}
	if e = contextError(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func identityQuery(q orm.Query[*models.MapRecord], r Record) orm.Query[*models.MapRecord] {
	for n, v := range r.PK {
		q = q.Filter(orm.Q(n, v))
	}
	return q.OrderBy()
}
func verify(ctx context.Context, b *operationBackend, p profile, q orm.Query[*models.MapRecord], expected Record) (Record, error) {
	rows, e := readRows(ctx, b.tx, b.dialect, p, identityQuery(q, expected), 2)
	if e != nil {
		return Record{}, e
	}
	if len(rows) != 1 {
		return Record{}, ErrForbidden
	}
	a, e := makeWire(p, expected)
	if e != nil {
		return Record{}, e
	}
	z, e := makeWire(p, rows[0])
	if e != nil {
		return Record{}, e
	}
	ab, _ := json.Marshal(a)
	zb, _ := json.Marshal(z)
	if !bytes.Equal(ab, zb) {
		return Record{}, ErrUnavailable
	}
	return rows[0], nil
}
