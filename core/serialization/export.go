package serialization

import (
	"context"
	"encoding/json"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"io"
)

// Dump emits bounded, deterministic model/primary-key ordered pages from one
// repeatable-read snapshot. A failed writer/read/commit can leave partial bytes;
// Complete is false and the stream must be discarded, never resumed or retried
// into the same writer. No diagnostic text is appended to fixture output.
func (f *Fixtures) Dump(ctx context.Context, w io.Writer, o DumpOptions) (result DumpResult, err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if err != nil {
			result.Complete = false
			err = safeError(err)
		}
	}()
	if f == nil || f.state == nil {
		return result, ErrConfiguration
	}
	s := *f.state
	selected, e := s.selectModels(o.Format, o.Models, false)
	if e != nil {
		return result, e
	}
	format := o.Format
	if nilValue(w) {
		return result, ErrInvalid
	}
	if e = contextError(ctx); e != nil {
		return result, e
	}
	if db.InTransaction(ctx, s.alias) {
		return result, ErrTransaction
	}
	b, store := s.operation(false)
	write := func(data []byte) error {
		if len(data) > s.limits.MaxBytes-result.Bytes {
			return ErrLimit
		}
		if e := contextError(ctx); e != nil {
			return e
		}
		n, e := w.Write(data)
		if n < 0 || n > len(data) {
			return ErrUnavailable
		}
		result.Bytes += n
		if e != nil || n != len(data) {
			return ErrUnavailable
		}
		return contextError(ctx)
	}
	var bodyErr error
	err = db.Atomic(ctx, b, atomicOptions(false), func(txctx context.Context) (operationErr error) {
		defer func() { bodyErr = operationErr }()
		queries := make([]orm.Query[*models.MapRecord], len(selected))
		for i, p := range selected {
			q, e := query(txctx, store, p)
			if e != nil {
				return e
			}
			queries[i] = q
		}
		if format == JSON {
			if e := write([]byte("[")); e != nil {
				return e
			}
		}
		seen := map[string]bool{}
		for i, p := range selected {
			offset := 0
			for {
				limit := min(64, s.limits.MaxRecords-result.Records+1)
				rows, e := readRows(txctx, b.tx, b.dialect, p, queries[i].Offset(offset), limit)
				if e != nil {
					return e
				}
				for _, r := range rows {
					if result.Records == s.limits.MaxRecords {
						return ErrLimit
					}
					if e := authorize(txctx, p, ExportRecord, r); e != nil {
						return &Error{Record: result.Records + 1, kind: e}
					}
					wire, e := makeWire(p, r)
					if e != nil {
						return e
					}
					raw, e := json.Marshal(wire)
					if e != nil {
						return ErrInvalid
					}
					if len(raw) > s.limits.MaxRecordBytes {
						return ErrLimit
					}
					key, _ := json.Marshal(wire.PK)
					id := wire.Model + "\x00" + string(key)
					if seen[id] {
						return ErrUnavailable
					}
					seen[id] = true
					if format == JSONL {
						raw = append(raw, '\n')
					} else if result.Records > 0 {
						raw = append([]byte{','}, raw...)
					}
					if e = write(raw); e != nil {
						return e
					}
					result.Records++
				}
				offset += len(rows)
				if len(rows) < limit {
					break
				}
			}
		}
		if format == JSON {
			return write([]byte("]\n"))
		}
		return nil
	})
	if err == nil {
		result.Complete = true
	} else if err != bodyErr && err != context.Canceled && err != context.DeadlineExceeded {
		err = ErrUnavailable
	}
	return result, err
}
