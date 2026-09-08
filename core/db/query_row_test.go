package db

import (
	"context"
	"errors"
	"testing"
)

type queryRowExecutor struct {
	Executor
	rows Rows
	err  error
}

func (e queryRowExecutor) Query(context.Context, string, ...any) (Rows, error) { return e.rows, e.err }

type queryRowFixture struct {
	Rows
	count, next, scans, closes                   int
	scanErr, initialErr, closeErr, afterCloseErr error
}

func (r *queryRowFixture) Next() bool { r.next++; return r.next <= r.count }
func (r *queryRowFixture) Scan(dest ...any) error {
	r.scans++
	if r.scanErr != nil {
		return r.scanErr
	}
	*dest[0].(*int) = 7
	return nil
}
func (r *queryRowFixture) Err() error {
	if r.closes > 0 && r.afterCloseErr != nil {
		return r.afterCloseErr
	}
	return r.initialErr
}
func (r *queryRowFixture) Close() error { r.closes++; return r.closeErr }

func TestQueryRowWaitsForTerminalOutcome(t *testing.T) {
	first := errors.New("first provider error")
	late := errors.New("late provider error")
	for _, test := range []struct {
		name     string
		rows     queryRowFixture
		queryErr error
		want     []error
	}{
		{"one", queryRowFixture{count: 1}, nil, nil},
		{"multiple first only", queryRowFixture{count: 100}, nil, nil},
		{"none", queryRowFixture{}, nil, []error{ErrNoRows}},
		{"late close", queryRowFixture{count: 1, closeErr: late}, nil, []error{late}},
		{"late err", queryRowFixture{count: 1, afterCloseErr: late}, nil, []error{late}},
		{"late both", queryRowFixture{count: 1, closeErr: first, afterCloseErr: late}, nil, []error{first, late}},
		{"scan plus close", queryRowFixture{count: 1, scanErr: first, closeErr: late}, nil, []error{first, late}},
		{"scan plus late err", queryRowFixture{count: 1, scanErr: first, afterCloseErr: late}, nil, []error{first, late}},
		{"initial err", queryRowFixture{initialErr: first}, nil, []error{first}},
		{"none plus close", queryRowFixture{closeErr: late}, nil, []error{late}},
		{"none plus late err", queryRowFixture{afterCloseErr: late}, nil, []error{late}},
		{"partial query", queryRowFixture{closeErr: late}, first, []error{first, late}},
		{"canceled terminal", queryRowFixture{count: 1, closeErr: context.Canceled}, nil, []error{context.Canceled}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := test.rows
			value := 0
			err := QueryRow(context.Background(), queryRowExecutor{rows: &rows, err: test.queryErr}, "fixture", nil, &value)
			if len(test.want) == 0 && (err != nil || value != 7) {
				t.Fatal(value, err)
			}
			for _, want := range test.want {
				if !errors.Is(err, want) {
					t.Fatal("missing failure", want, err)
				}
			}
			if test.name != "none" && errors.Is(err, ErrNoRows) {
				t.Fatal("provider failure became a miss", err)
			}
			if rows.closes != 1 || rows.next > 1 || rows.scans > 1 {
				t.Fatal("unbounded or leaked result", rows)
			}
			if test.queryErr != nil && (rows.next != 0 || rows.scans != 0) {
				t.Fatal("consumed failed query", rows)
			}
		})
	}
}

func TestQueryRowNilStreamAndQueryErrors(t *testing.T) {
	want := errors.New("query failed")
	for _, rows := range []Rows{nil, (*queryRowFixture)(nil)} {
		var value int
		if err := QueryRow(context.Background(), queryRowExecutor{rows: rows}, "fixture", nil, &value); !IsCode(err, Unavailable) {
			t.Fatal(err)
		}
		if err := QueryRow(context.Background(), queryRowExecutor{rows: rows, err: want}, "fixture", nil, &value); err != want {
			t.Fatal(err)
		}
	}
}
