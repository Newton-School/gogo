package serialization

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type fixtureDialect struct{}

func (fixtureDialect) Name() string { return "fixture" }
func (fixtureDialect) QuoteIdentifier(s string) (string, error) {
	if !models.ValidIdentifier(s) {
		return "", ErrInvalid
	}
	return `"` + s + `"`, nil
}
func (fixtureDialect) Placeholder(i int) string               { return "$" + strconv.Itoa(i) }
func (fixtureDialect) FieldType(models.Field) (string, error) { return "TEXT", nil }

type fixtureBackend struct {
	db.Backend
	rows                                                 [][]any
	alias                                                string
	begins, queries, inserts, checks, commits, rollbacks int
	options                                              db.TxOptions
	tx                                                   *fixtureTx
	missingCheck                                         bool
	commitErr, rollbackErr, checkErr                     error
	commitApplied, commitPanic                           bool
	queryHook                                            func(string, []any)
	checkHook, commitHook, beginHook                     func()
	rowHook                                              func(*fixtureRows)
}

func (b *fixtureBackend) Alias() string {
	if b.alias != "" {
		return b.alias
	}
	return "fixtures"
}
func (b *fixtureBackend) Dialect() db.Dialect           { return fixtureDialect{} }
func (b *fixtureBackend) Capabilities() db.Capabilities { return db.Capabilities{"transactions": true} }
func (b *fixtureBackend) BeginTx(_ context.Context, o db.TxOptions) (db.Transaction, error) {
	b.begins++
	b.options = o
	t := &fixtureTx{b: b, rows: cloneRows(b.rows)}
	b.tx = t
	if b.beginHook != nil {
		b.beginHook()
	}
	if b.missingCheck {
		return fixturePlainTx{Transaction: t}, nil
	}
	return t, nil
}

type fixturePlainTx struct{ db.Transaction }
type fixtureTx struct {
	b    *fixtureBackend
	rows [][]any
}

func (t *fixtureTx) Exec(context.Context, string, ...any) (db.Result, error) {
	return nil, ErrUnavailable
}
func (t *fixtureTx) Query(_ context.Context, statement string, args ...any) (db.Rows, error) {
	b := t.b
	b.queries++
	if b.queryHook != nil {
		b.queryHook(statement, args)
	}
	var found [][]any
	if strings.HasPrefix(statement, "INSERT") {
		b.inserts++
		for _, r := range t.rows {
			if reflect.DeepEqual(r[0], args[0]) {
				return nil, &db.Error{Code: db.UniqueViolation}
			}
		}
		v := cloneRows([][]any{args})[0]
		t.rows = append(t.rows, v)
		found = [][]any{v}
	} else {
		limitIndex := len(args) - 1
		offset := 0
		if strings.Contains(statement, " OFFSET ") {
			offset = args[len(args)-1].(int)
			limitIndex--
		}
		limit := args[limitIndex].(int)
		for _, r := range t.rows {
			arg := 0
			if strings.Contains(statement, `"tenant" =`) {
				if r[2] != args[arg] {
					continue
				}
				arg++
			}
			if strings.Contains(statement, `"id" =`) && !reflect.DeepEqual(r[0], args[arg]) {
				continue
			}
			found = append(found, r)
		}
		sort.Slice(found, func(i, j int) bool { return found[i][0].(int64) < found[j][0].(int64) })
		if offset >= len(found) {
			found = nil
		} else {
			found = found[offset:]
			found = found[:min(len(found), limit)]
		}
	}
	r := &fixtureRows{rows: found}
	if b.rowHook != nil {
		b.rowHook(r)
	}
	return r, nil
}
func (t *fixtureTx) CheckConstraints(context.Context) error {
	t.b.checks++
	if t.b.checkHook != nil {
		t.b.checkHook()
	}
	return t.b.checkErr
}
func (t *fixtureTx) Commit() error {
	b := t.b
	b.commits++
	if b.commitApplied || b.commitErr == nil && !b.commitPanic {
		b.rows = cloneRows(t.rows)
	}
	if b.commitHook != nil {
		b.commitHook()
	}
	if b.commitPanic {
		panic("private commit")
	}
	return b.commitErr
}
func (t *fixtureTx) Rollback() error { t.b.rollbacks++; return t.b.rollbackErr }

type fixtureRows struct {
	rows                  [][]any
	index                 int
	closeErr, terminalErr error
	closeHook, scanHook   func()
	closed                int
}

func (r *fixtureRows) Columns() ([]string, error) { return nil, nil }
func (r *fixtureRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	r.index++
	return true
}
func (r *fixtureRows) Scan(dest ...any) error {
	for i, d := range dest {
		*d.(*any) = r.rows[r.index-1][i]
	}
	if r.scanHook != nil {
		r.scanHook()
	}
	return nil
}
func (r *fixtureRows) Err() error {
	if r.index >= len(r.rows) {
		return r.terminalErr
	}
	return nil
}
func (r *fixtureRows) Close() error {
	r.closed++
	if r.closeHook != nil {
		r.closeHook()
	}
	return r.closeErr
}
func cloneRows(in [][]any) [][]any {
	out := make([][]any, len(in))
	for i, r := range in {
		out[i] = append([]any(nil), r...)
	}
	return out
}

func fixtureProfile() ModelProfile {
	return ModelProfile{
		Schema: models.Schema{AppLabel: "example", Name: "Note", Fields: []models.Field{{Name: "id", Kind: models.BigInteger, PrimaryKey: true}, {Name: "title", Kind: models.Text}, {Name: "tenant", Kind: models.Char}}},
		Fields: []string{"id", "title", "tenant"}, Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", "allowed"), nil }, Authorize: func(context.Context, Action, Record) error { return nil }, Import: true,
	}
}
func fixture(t *testing.T, b *fixtureBackend, p ModelProfile) *Fixtures {
	t.Helper()
	f, e := New(Config{Backend: b, Profiles: []ModelProfile{p}})
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func fixtureWire(id int, title, tenant string) string {
	return fmt.Sprintf(`{"version":1,"model":"example.Note","pk":{"id":{"value":%d}},"fields":{"tenant":{"value":%q},"title":{"value":%q}}}`, id, tenant, title)
}
func loadOptions() LoadOptions { return LoadOptions{Format: JSON, Models: []string{"example.Note"}} }
func dumpOptions() DumpOptions { return DumpOptions{Format: JSON, Models: []string{"example.Note"}} }

func TestFixturesRoundTripScopeAndRawPolicy(t *testing.T) {
	for _, format := range []Format{JSON, JSONL} {
		t.Run(string(format), func(t *testing.T) {
			b := &fixtureBackend{rows: [][]any{{int64(2), "two", "allowed"}, {int64(1), "one", "allowed"}, {int64(3), "private", "hidden"}}}
			p := fixtureProfile()
			p.Schema.Fields[1].DefaultFunc = func() any { panic("default executed") }
			p.Schema.Fields[1].AutoNow = true
			p.Schema.Fields[1].Validators = []models.Validator{func(context.Context, any) error { panic("validator executed") }}
			grants := 0
			p.Authorize = func(_ context.Context, _ Action, r Record) error {
				grants++
				r.PK["id"] = int64(999)
				r.Fields["title"] = "changed"
				return nil
			}
			f := fixture(t, b, p)
			var out bytes.Buffer
			o := dumpOptions()
			o.Format = format
			result, e := f.Dump(context.Background(), &out, o)
			if e != nil || !result.Complete || result.Records != 2 || result.Bytes != out.Len() || b.options.Isolation != sql.LevelRepeatableRead || !b.options.ReadOnly || grants != 2 {
				t.Fatal(result, e, grants)
			}
			if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "changed") || strings.Index(out.String(), "one") > strings.Index(out.String(), "two") {
				t.Fatal(out.String())
			}
			target := &fixtureBackend{}
			g := fixture(t, target, p)
			opts := loadOptions()
			opts.Format = format
			loaded, e := g.Load(context.Background(), bytes.NewReader(out.Bytes()), opts)
			if e != nil || loaded.Records != 2 || !loaded.Committed || loaded.DryRun || target.checks != 1 || target.commits != 1 || len(target.rows) != 2 || target.rows[0][1] != "one" {
				t.Fatal(loaded, e, target)
			}
			var again bytes.Buffer
			if _, e = g.Dump(context.Background(), &again, o); e != nil || again.String() != out.String() {
				t.Fatal(again.String(), e)
			}
		})
	}
}

func TestFixturesConstructorOwnershipAndRefusals(t *testing.T) {
	for _, mode := range []string{"auto-import", "missing-pk", "missing-column", "codec", "relation", "generated", "db-default", "duplicate", "nil-scope", "nil-grant", "invalid-bounds"} {
		t.Run(mode, func(t *testing.T) {
			b := &fixtureBackend{}
			p := fixtureProfile()
			c := Config{Backend: b, Profiles: []ModelProfile{p}}
			switch mode {
			case "auto-import":
				p.Schema.Fields[0].Kind = models.BigAuto
			case "missing-pk":
				p.Fields = []string{"title", "tenant"}
			case "missing-column":
				p.Fields = []string{"id", "title"}
			case "codec":
				p.Schema.Fields[1].Codec = fixtureCodec{}
			case "relation":
				p.Schema.Fields[1].Relation = &models.Relation{Target: "example.Other"}
			case "generated":
				p.Schema.Fields[1].GeneratedExpression = "private()"
			case "db-default":
				p.Schema.Fields[1].DBDefault = "private()"
			case "duplicate":
				p.Fields = append(p.Fields, "id")
			case "nil-scope":
				p.Scope = nil
			case "nil-grant":
				p.Authorize = nil
			case "invalid-bounds":
				c.Limits.MaxRecords = MaxRecords + 1
			}
			c.Profiles[0] = p
			if _, e := New(c); e != ErrConfiguration || b.queries != 0 || b.begins != 0 {
				t.Fatal(e, b)
			}
		})
	}
	b := &fixtureBackend{rows: [][]any{{int64(1), "title", "allowed"}}}
	p := fixtureProfile()
	p.Import = false
	p.Schema.Fields[0].Kind = models.BigAuto
	p.Fields = []string{"id", "title"}
	p.PolicyFields = []string{"tenant"}
	f := fixture(t, b, p)
	p.Fields[0] = "tenant"
	p.Schema.Fields[0].Name = "retarget"
	p.PolicyFields[0] = "private"
	b.alias = "changed"
	var out bytes.Buffer
	if result, e := f.Dump(context.Background(), &out, dumpOptions()); e != nil || !result.Complete || f.Alias() != "fixtures" || strings.Contains(out.String(), "allowed") {
		t.Fatal(out.String(), e, result)
	}
	if _, e := f.Load(context.Background(), strings.NewReader("[]"), loadOptions()); e != ErrInvalid {
		t.Fatal(e)
	}
}

type fixtureCodec struct{}

func (fixtureCodec) Encode(any) (any, error) { panic("codec executed") }
func (fixtureCodec) Decode(any) (any, error) { panic("codec executed") }

func TestFixturesImportRollbackAndConstraintOrder(t *testing.T) {
	for _, mode := range []string{"denied", "scope", "conflict", "constraint", "trigger", "last-grant-drift", "dry", "dry-check-error", "dry-rollback-error", "missing-capability"} {
		t.Run(mode, func(t *testing.T) {
			b := &fixtureBackend{}
			p := fixtureProfile()
			calls := 0
			p.Authorize = func(_ context.Context, _ Action, r Record) error {
				calls++
				if mode == "denied" {
					return ErrForbidden
				}
				if mode == "last-grant-drift" && calls == 4 {
					b.tx.rows[0][1] = "changed"
				}
				return nil
			}
			switch mode {
			case "conflict":
				b.rows = [][]any{{int64(1), "old", "allowed"}}
			case "constraint", "dry-check-error":
				b.checkErr = errors.New("private constraint")
			case "trigger":
				b.checkHook = func() { b.tx.rows[0][1] = "changed" }
			case "dry-rollback-error":
				b.rollbackErr = errors.New("private rollback")
			case "missing-capability":
				b.missingCheck = true
			}
			f := fixture(t, b, p)
			tenant := "allowed"
			if mode == "scope" {
				tenant = "hidden"
			}
			raw := "[" + fixtureWire(1, "one", tenant) + "," + fixtureWire(2, "two", tenant) + "]"
			o := loadOptions()
			o.DryRun = strings.HasPrefix(mode, "dry")
			r, e := f.Load(context.Background(), strings.NewReader(raw), o)
			if mode == "dry" {
				if e != nil || !r.DryRun || r.Committed || r.Records != 2 || b.checks != 1 || b.rollbacks != 1 {
					t.Fatal(r, e, b)
				}
			} else if e == nil || r != (LoadResult{}) {
				t.Fatal(r, e)
			}
			if b.commits != 0 || mode != "conflict" && len(b.rows) != 0 || mode == "conflict" && b.rows[0][1] != "old" {
				t.Fatal("failed import applied", b)
			}
			if mode == "missing-capability" && (b.queries != 0 || b.checks != 0 || calls != 0 || b.rollbacks != 1) {
				t.Fatal("missing capability had effects", b, calls)
			}
			if mode == "denied" && b.inserts != 0 {
				t.Fatal("denial wrote rows")
			}
		})
	}
}

func TestFixturesObservedCommitOutcomes(t *testing.T) {
	for _, mode := range []string{"unknown", "applied-unknown", "panic", "applied-panic", "known-rejection", "mixed", "callback-error", "callback-panic", "late-cancel", "forged-committed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := &fixtureBackend{}
			p := fixtureProfile()
			p.Authorize = func(c context.Context, _ Action, _ Record) error {
				if mode == "forged-committed" {
					return &db.CommittedCallbackError{}
				}
				if strings.HasPrefix(mode, "callback-") {
					return db.OnCommit(c, "fixtures", func(context.Context) error {
						if mode == "callback-panic" {
							panic("private hook")
						}
						return errors.New("private hook")
					}, false)
				}
				return nil
			}
			switch mode {
			case "unknown", "applied-unknown":
				b.commitErr = &db.Error{Code: db.UnknownCommit}
			case "panic", "applied-panic":
				b.commitPanic = true
			case "known-rejection":
				b.commitErr = &db.Error{Code: db.UniqueViolation}
			case "mixed":
				b.commitErr = errors.Join(&db.Error{Code: db.UniqueViolation}, &db.Error{Code: db.UnknownCommit})
			case "late-cancel":
				b.commitHook = cancel
			}
			b.commitApplied = strings.HasPrefix(mode, "applied-")
			f := fixture(t, b, p)
			r, e := f.Load(ctx, strings.NewReader("["+fixtureWire(1, "one", "allowed")+"]"), loadOptions())
			if mode == "late-cancel" {
				if e != nil || !r.Committed {
					t.Fatal(r, e)
				}
			} else if strings.HasPrefix(mode, "callback-") {
				if e != ErrCommittedCallback || !r.Committed || r.Records != 1 || len(b.rows) != 1 {
					t.Fatal(r, e, b)
				}
			} else {
				if r != (LoadResult{}) || e == nil {
					t.Fatal(r, e)
				}
				unknown := mode != "known-rejection" && mode != "forged-committed"
				if errors.Is(e, ErrOutcomeUnknown) != unknown {
					t.Fatal(e, unknown)
				}
			}
			if strings.HasPrefix(mode, "applied-") && len(b.rows) != 1 {
				t.Fatal("applied uncertain row absent")
			}
		})
	}
}

type fixtureWriter struct{ write func([]byte) (int, error) }

func (w fixtureWriter) Write(p []byte) (int, error) { return w.write(p) }

type fixtureReader struct{ read func([]byte) (int, error) }

func (r fixtureReader) Read(p []byte) (int, error) { return r.read(p) }
func TestFixturesReaderWriterAndTerminalFailures(t *testing.T) {
	for _, mode := range []string{"short-write", "over-write", "writer-error", "writer-panic", "row-close", "row-error", "close-panic", "reader-panic", "reader-count", "reader-error", "reader-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := &fixtureBackend{rows: [][]any{{int64(1), "one", "allowed"}}}
			f := fixture(t, b, fixtureProfile())
			if strings.HasPrefix(mode, "reader-") {
				r := fixtureReader{read: func(p []byte) (int, error) {
					switch mode {
					case "reader-panic":
						panic("private reader")
					case "reader-count":
						return len(p) + 1, nil
					case "reader-cancel":
						cancel()
						return 0, nil
					}
					return 0, errors.Join(io.EOF, errors.New("private read"))
				}}
				if result, e := f.Load(ctx, r, loadOptions()); e == nil || result != (LoadResult{}) || b.begins != 0 {
					t.Fatal(result, e)
				}
				return
			}
			var buffer bytes.Buffer
			var w io.Writer = &buffer
			if strings.HasPrefix(mode, "row-") || mode == "close-panic" {
				b.rowHook = func(r *fixtureRows) {
					switch mode {
					case "row-close":
						r.closeErr = errors.New("private close")
					case "row-error":
						r.terminalErr = errors.New("private terminal")
					case "close-panic":
						r.closeHook = func() { panic("private close") }
					}
				}
			} else {
				w = fixtureWriter{write: func(p []byte) (int, error) {
					switch mode {
					case "short-write":
						return len(p) - 1, nil
					case "over-write":
						return len(p) + 1, nil
					case "writer-panic":
						panic("private writer")
					}
					return 0, errors.New("private write")
				}}
			}
			result, e := f.Dump(ctx, w, dumpOptions())
			if e == nil || result.Complete || b.commits != 0 || strings.Contains(fmt.Sprintf("%#v", e), "private") {
				t.Fatal(result, e)
			}
		})
	}
}

func TestFixturesPaginationAndExplicitLimits(t *testing.T) {
	b := &fixtureBackend{}
	for i := 130; i > 0; i-- {
		b.rows = append(b.rows, []any{int64(i), "title", "allowed"})
	}
	f := fixture(t, b, fixtureProfile())
	var out bytes.Buffer
	r, e := f.Dump(context.Background(), &out, dumpOptions())
	if e != nil || r.Records != 130 || b.queries != 3 {
		t.Fatal(r, e, b.queries)
	}
	f, e = New(Config{Backend: b, Profiles: []ModelProfile{fixtureProfile()}, Limits: Limits{MaxRecords: 3}})
	if e != nil {
		t.Fatal(e)
	}
	out.Reset()
	r, e = f.Dump(context.Background(), &out, dumpOptions())
	if e != ErrLimit || r.Records != 3 || r.Complete {
		t.Fatal(r, e)
	}
}

func TestFixturesMissingCapabilityCleanupAndDiagnosticProvenance(t *testing.T) {
	for _, cleanup := range []error{nil, errors.New("private rollback"), &Error{Record: 999, Field: "private-field", kind: ErrForbidden}} {
		b := &fixtureBackend{missingCheck: true, rollbackErr: cleanup}
		f := fixture(t, b, fixtureProfile())
		r, e := f.Load(context.Background(), strings.NewReader("[]"), loadOptions())
		want := ErrConfiguration
		if cleanup != nil {
			want = ErrUnavailable
		}
		if e != want || r != (LoadResult{}) || b.queries != 0 || b.rollbacks != 1 {
			t.Fatal(r, e, want, b)
		}
	}
	// An exported diagnostic-shaped value from a provider cannot manufacture
	// a trusted record/field location, even if its private kind is set by this
	// same-package hostile-provider fixture.
	b := &fixtureBackend{rows: [][]any{{int64(1), "one", "allowed"}}, commitErr: &Error{Record: 999, Field: "private-field", kind: ErrForbidden}}
	f := fixture(t, b, fixtureProfile())
	var out bytes.Buffer
	r, e := f.Dump(context.Background(), &out, dumpOptions())
	var diagnostic *Error
	if e != ErrUnavailable || r.Complete || errors.As(e, &diagnostic) {
		t.Fatal(r, e)
	}
	ctx := &fixtureMutationContext{Context: context.Background()}
	calls := 0
	ctx.hook = func() {
		calls++
		if calls == 3 {
			ctx.err = &Error{Record: 999, Field: "private-field", kind: ErrForbidden}
		}
	}
	b = &fixtureBackend{}
	f = fixture(t, b, fixtureProfile())
	if _, e := f.Load(ctx, strings.NewReader("[]"), loadOptions()); e == nil || errors.As(e, &diagnostic) {
		t.Fatal(e)
	}
}

func TestFixturesExpectedSnapshotDoesNotAliasInsertArguments(t *testing.T) {
	p := fixtureProfile()
	p.Schema.Fields = append(p.Schema.Fields, models.Field{Name: "blob", Kind: models.Binary})
	p.Fields = append(p.Fields, "blob")
	b := &fixtureBackend{}
	b.queryHook = func(statement string, args []any) {
		if strings.HasPrefix(statement, "INSERT") {
			args[3].([]byte)[0] = 9
		}
	}
	f := fixture(t, b, p)
	raw := strings.Replace(fixtureWire(1, "one", "allowed"), `"fields":{`, `"fields":{"blob":{"value":"AQI="},`, 1)
	r, e := f.Load(context.Background(), strings.NewReader("["+raw+"]"), loadOptions())
	if e == nil || r != (LoadResult{}) || len(b.rows) != 0 || b.inserts != 1 || b.rollbacks != 1 {
		t.Fatal(raw, r, e, b)
	}
}

func TestFixturesAmbientTransactionBoundaries(t *testing.T) {
	b := &fixtureBackend{}
	f := fixture(t, b, fixtureProfile())
	e := db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		r, e := f.Load(ctx, strings.NewReader("[]"), loadOptions())
		if e != ErrTransaction || r != (LoadResult{}) || b.begins != 1 {
			t.Fatal(r, e, b.begins)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	other := &fixtureBackend{alias: "other"}
	e = db.Atomic(context.Background(), other, db.AtomicOptions{}, func(ctx context.Context) error {
		r, e := f.Load(ctx, strings.NewReader("[]"), loadOptions())
		if e != nil || !r.Committed || !db.InTransaction(ctx, "other") {
			t.Fatal(r, e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
