package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type fileServiceDialect struct{}

func (fileServiceDialect) Name() string                           { return "test" }
func (fileServiceDialect) Placeholder(i int) string               { return fmt.Sprintf("$%d", i) }
func (fileServiceDialect) FieldType(models.Field) (string, error) { return "text", nil }
func (fileServiceDialect) QuoteIdentifier(s string) (string, error) {
	if !models.ValidIdentifier(s) {
		return "", ErrConfiguration
	}
	return `"` + s + `"`, nil
}

type fileServiceBackend struct {
	db.Backend
	owner                   []any
	files                   map[string][]any
	tx                      *fileServiceTx
	begins, queries, writes int
	aliasHook               func()
	beginHook               func(db.TxOptions) (db.Transaction, error)
	queryHook               func(*fileServiceTx, string, []any) (db.Rows, error, bool)
	commitHook              func(*fileServiceTx) error
}

func (b *fileServiceBackend) Alias() string {
	if b.aliasHook != nil {
		b.aliasHook()
	}
	return "files"
}
func (*fileServiceBackend) Dialect() db.Dialect { return fileServiceDialect{} }
func (*fileServiceBackend) Capabilities() db.Capabilities {
	return db.Capabilities{"transactions": true, "row_locks": true}
}
func (b *fileServiceBackend) Query(context.Context, string, ...any) (db.Rows, error) {
	panic("query escaped owned transaction")
}
func (b *fileServiceBackend) BeginTx(_ context.Context, options db.TxOptions) (db.Transaction, error) {
	b.begins++
	if b.beginHook != nil {
		return b.beginHook(options)
	}
	tx := &fileServiceTx{b: b, options: options, owner: append([]any(nil), b.owner...), files: map[string][]any{}}
	for id, v := range b.files {
		tx.files[id] = append([]any(nil), v...)
	}
	b.tx = tx
	return tx, nil
}

type fileServiceTx struct {
	db.Transaction
	b               *fileServiceBackend
	options         db.TxOptions
	owner           []any
	files           map[string][]any
	closes, commits int
}

func (tx *fileServiceTx) Query(_ context.Context, statement string, args ...any) (db.Rows, error) {
	tx.b.queries++
	if tx.b.queryHook != nil {
		if rows, err, handled := tx.b.queryHook(tx, statement, args); handled {
			return rows, err
		}
	}
	rows := &fileServiceRows{}
	switch {
	case strings.HasPrefix(statement, `INSERT INTO "gogo_files"`):
		tx.b.writes++
		if tx.options.ReadOnly {
			return nil, ErrUnavailable
		}
		id := args[0].(string)
		if _, ok := tx.files[id]; ok {
			return nil, &db.Error{Code: db.UniqueViolation}
		}
		tx.files[id] = append([]any(nil), args...)
		rows.values = [][]any{tx.files[id]}
	case strings.HasPrefix(statement, `UPDATE "files_owner"`):
		tx.b.writes++
		tx.owner[2] = args[0]
		rows.values = [][]any{tx.owner}
	case strings.HasPrefix(statement, `UPDATE "gogo_files"`):
		tx.b.writes++
		id := args[1].(string)
		v, ok := tx.files[id]
		if !ok {
			return rows, nil
		}
		v[4] = args[0]
		rows.values = [][]any{v}
	case strings.Contains(statement, `FROM "files_owner"`):
		matchID, matchScope := false, false
		for _, arg := range args {
			matchID = matchID || arg == int64(1)
			matchScope = matchScope || arg == "one"
		}
		if len(tx.owner) > 0 && matchID && matchScope {
			rows.values = [][]any{tx.owner}
		}
	case strings.Contains(statement, `FROM "gogo_files"`):
		// The real compiler binds the LIMIT as its last parameter.
		if len(args) > 0 {
			if limit, ok := args[len(args)-1].(int); ok && limit == 2 {
				args = args[:len(args)-1]
			}
		}
		for _, v := range tx.files {
			if len(args) == 1 && args[0] == v[0] || len(args) == 2 && args[0] == v[1] && args[1] == v[2] {
				rows.values = append(rows.values, v)
			}
		}
	default:
		return nil, errors.New("unexpected test statement")
	}
	return rows, nil
}
func (tx *fileServiceTx) Commit() error {
	tx.commits++
	if tx.b.commitHook != nil {
		if err := tx.b.commitHook(tx); err != nil {
			return err
		}
	}
	if !tx.options.ReadOnly {
		tx.b.owner, tx.b.files = tx.owner, tx.files
	}
	return nil
}
func (tx *fileServiceTx) Rollback() error { tx.closes++; return nil }

type fileServiceRows struct {
	db.Rows
	values                 [][]any
	i, closes              int
	err, closeErr, scanErr error
	errPanic, closePanic   bool
	closeHook              func()
}

func (r *fileServiceRows) Next() bool {
	if r.i >= len(r.values) {
		return false
	}
	r.i++
	return true
}
func (r *fileServiceRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	if len(dest) != len(r.values[r.i-1]) {
		return errors.New("fixture column mismatch")
	}
	for i, value := range r.values[r.i-1] {
		target := reflect.ValueOf(dest[i]).Elem()
		if value == nil {
			target.SetZero()
			continue
		}
		v := reflect.ValueOf(value)
		if target.Kind() == reflect.Interface {
			target.Set(v)
		} else if v.Type().AssignableTo(target.Type()) {
			target.Set(v)
		} else if v.Type().ConvertibleTo(target.Type()) {
			target.Set(v.Convert(target.Type()))
		} else if target.Kind() == reflect.Pointer && v.Type().AssignableTo(target.Type().Elem()) {
			copy := reflect.New(target.Type().Elem())
			copy.Elem().Set(v)
			target.Set(copy)
		} else {
			return errors.New("fixture value mismatch")
		}
	}
	return nil
}
func (r *fileServiceRows) Err() error {
	if r.errPanic {
		panic("private provider")
	}
	return r.err
}
func (r *fileServiceRows) Close() error {
	r.closes++
	if r.closeHook != nil {
		r.closeHook()
	}
	if r.closePanic {
		panic("private close")
	}
	return r.closeErr
}

type fileServiceStorage struct {
	Storage
	blobs        map[string][]byte
	saves, opens int
	saveHook     func(context.Context, string, io.Reader) (SaveResult, error)
	openHook     func(context.Context, string) (Reader, error)
}

func (s *fileServiceStorage) Save(ctx context.Context, key string, source io.Reader) (SaveResult, error) {
	s.saves++
	if s.saveHook != nil {
		return s.saveHook(ctx, key, source)
	}
	if _, ok := s.blobs[key]; ok {
		return SaveResult{}, ErrCollision
	}
	data, err := io.ReadAll(source)
	if err != nil {
		return SaveResult{}, err
	}
	s.blobs[key] = data
	return SaveResult{Key: key, Bytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), ModifiedTime: serviceNow(), Published: true}, nil
}
func (s *fileServiceStorage) Open(ctx context.Context, key string) (Reader, error) {
	s.opens++
	if s.openHook != nil {
		return s.openHook(ctx, key)
	}
	data, ok := s.blobs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return &fileServiceReader{Reader: bytes.NewReader(data)}, nil
}

type fileServiceReader struct {
	*bytes.Reader
	closes   int
	closeErr error
}

func (r *fileServiceReader) Close() error { r.closes++; return r.closeErr }

func fileOwnerSchema() models.Schema {
	return models.Schema{AppLabel: "files_test", Name: "Owner", Table: "files_owner", Fields: []models.Field{models.BigIntegerField("id", models.Primary), models.TextField("tenant"), models.FileField("asset", models.Optional, models.WithMaxLength(32))}}
}
func fileServiceConfig(t *testing.T) (ServiceConfig, *fileServiceBackend, *fileServiceStorage) {
	t.Helper()
	registry := &models.Registry{}
	if err := registry.Register(fileOwnerSchema()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	b := &fileServiceBackend{owner: []any{int64(1), "one", ""}, files: map[string][]any{}}
	storage := &fileServiceStorage{blobs: map[string][]byte{}}
	c := ServiceConfig{Backend: b, Registry: registry, StorageAlias: "private", Storage: storage, Bindings: []OwnerBinding{{Name: "asset", Model: "files_test.Owner", Field: "asset", PolicyFields: []string{"tenant"}, Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", "one"), nil }, Authorize: func(context.Context, OwnerAction, OwnerSnapshot) error { return nil }}}}
	return c, b, storage
}
func fileServiceInput(t *testing.T) StoreInput {
	t.Helper()
	id, err := NewUploadIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return StoreInput{Identity: id, Owner: OwnerInput{Binding: "asset", Key: map[string]any{"id": int64(1)}}, ContentType: "text/plain"}
}
func mustFileService(t *testing.T, c ServiceConfig) *Service {
	t.Helper()
	s, e := NewService(c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestFileServiceStoreReplaceAndOpen(t *testing.T) {
	c, b, storage := fileServiceConfig(t)
	var actions []OwnerAction
	c.Bindings[0].Authorize = func(_ context.Context, action OwnerAction, snapshot OwnerSnapshot) error {
		actions = append(actions, action)
		if snapshot.Key["id"] != int64(1) || snapshot.Fields["tenant"] != "one" {
			t.Fatal(snapshot)
		}
		snapshot.Key["id"] = int64(9)
		snapshot.Fields["tenant"] = "changed"
		if snapshot.Previous != nil {
			snapshot.Previous.State = Deleted
		}
		return nil
	}
	s := mustFileService(t, c)
	first := fileServiceInput(t)
	info, err := s.StoreValidated(context.Background(), first, strings.NewReader("first"))
	if err != nil || info.ID != first.Identity.ID || b.owner[2] != first.Identity.Key || b.begins != 2 {
		t.Fatal(info, err, b.owner, b.begins)
	}
	opened, reader, err := s.Open(context.Background(), info.ID)
	if err != nil || opened.State != Ready {
		t.Fatal(opened, err)
	}
	body, _ := io.ReadAll(reader)
	if string(body) != "first" || reader.Close() != nil {
		t.Fatal(string(body))
	}
	second := fileServiceInput(t)
	next, err := s.StoreValidated(context.Background(), second, strings.NewReader("second"))
	if err != nil || next.ObjectKey != second.Identity.Key {
		t.Fatal(next, err)
	}
	if fmt.Sprint(b.files[info.ID][4]) != string(Deleting) || string(storage.blobs[info.ObjectKey]) != "first" {
		t.Fatal("old managed blob was removed or not retired")
	}
	before := storage.opens
	if _, reader, err := s.Open(context.Background(), info.ID); err != ErrNotFound || reader != nil || storage.opens != before {
		t.Fatal("non-ready file touched storage", err)
	}
	if !reflect.DeepEqual(actions, []OwnerAction{StoreFile, StoreFile, ReadFile, ReadFile, StoreFile, StoreFile, ReplaceFile}) {
		t.Fatal(actions)
	}
}

func TestFileServiceCommitAndPublicationOutcomes(t *testing.T) {
	for _, mode := range []string{"success", "late-cancel", "unknown", "mixed", "cause", "panic", "rejected", "save-panic", "save-malformed", "save-published-error"} {
		t.Run(mode, func(t *testing.T) {
			c, b, storage := fileServiceConfig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := error(nil)
			published, unknown := true, false
			b.commitHook = func(tx *fileServiceTx) error {
				if tx.options.ReadOnly {
					return nil
				}
				switch mode {
				case "late-cancel":
					cancel()
				case "unknown":
					return &db.Error{Code: db.UnknownCommit}
				case "mixed":
					return errors.Join(&db.Error{Code: db.UniqueViolation}, &db.Error{Code: db.UnknownCommit})
				case "cause":
					return &db.Error{Code: db.UniqueViolation, Cause: errors.New("network")}
				case "panic":
					panic("private commit")
				case "rejected":
					return &db.Error{Code: db.CheckViolation}
				}
				return nil
			}
			switch mode {
			case "unknown", "mixed", "cause", "panic":
				want = ErrOutcomeUnknown
			case "rejected":
				want = ErrUnavailable
			case "save-panic":
				want = ErrPublicationUnknown
				published = false
				unknown = true
				storage.saveHook = func(context.Context, string, io.Reader) (SaveResult, error) { panic("secret source") }
			case "save-malformed":
				want = ErrPublicationUnknown
				published = false
				unknown = true
				storage.saveHook = func(context.Context, string, io.Reader) (SaveResult, error) { return SaveResult{}, nil }
			case "save-published-error":
				want = ErrUnavailable
				storage.saveHook = func(_ context.Context, key string, _ io.Reader) (SaveResult, error) {
					return SaveResult{Key: key, Bytes: 0, SHA256: strings.Repeat("0", 64), ModifiedTime: serviceNow(), Published: true}, errors.New("private path")
				}
			}
			input := fileServiceInput(t)
			info, err := mustFileService(t, c).StoreValidated(ctx, input, strings.NewReader("data"))
			if !errors.Is(err, want) {
				t.Fatal(info, err, want)
			}
			if want == nil {
				if info.ID != input.Identity.ID {
					t.Fatal(info)
				}
				return
			}
			var failure *StoreFailure
			if info != (Info{}) || !errors.As(err, &failure) || failure.Identity != input.Identity || failure.Published != published || failure.PublicationUnknown != unknown {
				t.Fatal(info, err, failure)
			}
			if strings.Contains(fmt.Sprintf("%#v %+v %s", err, err, err), "private") {
				t.Fatal("unsafe provider error")
			}
		})
	}
}

func TestFileServiceDenialAndSnapshotBoundaries(t *testing.T) {
	for _, mode := range []string{"denied-before-source", "denied-after-source", "mixed-denial", "unmanaged", "foreign", "scope-miss", "drift", "input-snapshot", "scope-snapshot"} {
		t.Run(mode, func(t *testing.T) {
			c, b, storage := fileServiceConfig(t)
			input := fileServiceInput(t)
			calls := 0
			var service *Service
			c.Bindings[0].Authorize = func(_ context.Context, _ OwnerAction, _ OwnerSnapshot) error {
				calls++
				switch mode {
				case "denied-before-source":
					return ErrForbidden
				case "denied-after-source":
					if calls == 2 {
						return ErrForbidden
					}
				case "mixed-denial":
					return errors.Join(ErrForbidden, errors.New("provider"))
				case "drift":
					if calls == 2 {
						b.tx.owner[1] = "changed"
					}
				case "input-snapshot":
					input.Owner.Key["id"] = int64(9)
					*service = Service{}
				case "scope-snapshot":
					c.Bindings[0].Scope = func(context.Context, models.Schema) (db.Predicate, error) { panic("replaced scope") }
				}
				return nil
			}
			want := ErrForbidden
			switch mode {
			case "unmanaged", "foreign":
				b.owner[2] = strings.Repeat("a", 32)
				want = ErrMetadataConflict
			case "mixed-denial":
				want = ErrUnavailable
			case "scope-miss":
				input.Owner.Key["id"] = int64(2)
				want = ErrNotFound
			case "drift":
				want = ErrMetadataConflict
			case "input-snapshot", "scope-snapshot":
				want = nil
			}
			if mode == "foreign" {
				id := fileServiceInput(t).Identity.ID
				now := serviceNow()
				b.files[id] = []any{id, "private", b.owner[2], `{"v":1,"model":"other.Owner","field":"asset","key":[{"name":"id","value":"1"}]}`, Ready, "text/plain", int64(1), strings.Repeat("0", 64), now, &now}
			}
			service = mustFileService(t, c)
			info, err := service.StoreValidated(context.Background(), input, strings.NewReader("data"))
			if !errors.Is(err, want) {
				t.Fatal(info, err, want)
			}
			if want != nil && b.writes != 0 {
				t.Fatal("failed grant wrote database", b.writes)
			}
			if (mode == "denied-before-source" || mode == "mixed-denial" || mode == "scope-miss") && storage.saves != 0 {
				t.Fatal("source read before authority")
			}
		})
	}
}

func TestFileServiceRowCompletionAndOpenResourceCleanup(t *testing.T) {
	for _, mode := range []string{"close", "err", "scan", "duplicate", "err-panic", "close-panic", "rows-error", "typed-nil"} {
		t.Run(mode, func(t *testing.T) {
			c, b, storage := fileServiceConfig(t)
			rows := &fileServiceRows{values: [][]any{{int64(1), "one", ""}}}
			queryErr := error(nil)
			switch mode {
			case "close":
				rows.closeErr = errors.New("private close")
			case "err":
				rows.err = errors.New("private read")
			case "scan":
				rows.scanErr = errors.New("private scan")
			case "duplicate":
				rows.values = append(rows.values, rows.values[0])
			case "err-panic":
				rows.errPanic = true
			case "close-panic":
				rows.closePanic = true
			case "rows-error":
				queryErr = errors.New("partial query")
			case "typed-nil":
				rows = nil
			}
			b.queryHook = func(*fileServiceTx, string, []any) (db.Rows, error, bool) { return rows, queryErr, true }
			info, err := mustFileService(t, c).StoreValidated(context.Background(), fileServiceInput(t), strings.NewReader("data"))
			if !errors.Is(err, ErrUnavailable) || info != (Info{}) || storage.saves != 0 {
				t.Fatal(info, err)
			}
			if rows != nil && rows.closes != 1 {
				t.Fatal("rows leaked", rows.closes)
			}
		})
	}
	for _, mode := range []string{"partial-reader", "commit", "final-denial", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			c, b, storage := fileServiceConfig(t)
			deny := false
			c.Bindings[0].Authorize = func(context.Context, OwnerAction, OwnerSnapshot) error {
				if deny {
					return ErrForbidden
				}
				return nil
			}
			s := mustFileService(t, c)
			info, err := s.StoreValidated(context.Background(), fileServiceInput(t), strings.NewReader("data"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := &fileServiceReader{Reader: bytes.NewReader([]byte("data"))}
			storage.openHook = func(context.Context, string) (Reader, error) {
				switch mode {
				case "partial-reader":
					return reader, errors.New("private open")
				case "final-denial":
					deny = true
				case "canceled":
					cancel()
				}
				return reader, nil
			}
			if mode == "commit" {
				b.commitHook = func(*fileServiceTx) error { return errors.New("private commit") }
			}
			out, r, e := s.Open(ctx, info.ID)
			if e == nil || out != (Info{}) || r != nil || reader.closes != 1 {
				t.Fatal(out, r, e, reader.closes)
			}
		})
	}
}

func TestFileServiceRejectsAmbientBeforeStorageAndRollsBackPartialBegin(t *testing.T) {
	c, b, storage := fileServiceConfig(t)
	s := mustFileService(t, c)
	if err := db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		_, e := s.StoreValidated(ctx, fileServiceInput(t), strings.NewReader("data"))
		if !errors.Is(e, ErrTransaction) {
			t.Fatal(e)
		}
		_, r, e := s.Open(ctx, fileServiceInput(t).Identity.ID)
		if e != ErrTransaction || r != nil {
			t.Fatal(e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if storage.saves != 0 || storage.opens != 0 || b.begins != 1 {
		t.Fatal(b.begins, storage.saves, storage.opens)
	}
	partial := &fileServiceTx{}
	b.beginHook = func(db.TxOptions) (db.Transaction, error) { return partial, errors.New("partial resource") }
	_, err := s.StoreValidated(context.Background(), fileServiceInput(t), strings.NewReader("data"))
	if !errors.Is(err, ErrUnavailable) || partial.closes != 1 {
		t.Fatal(err, partial.closes)
	}
}

func TestFileServiceConstructorAndReferenceBounds(t *testing.T) {
	for _, mode := range []string{"nil-backend", "typed-nil", "nil-storage", "no-scope", "no-policy", "duplicate-binding", "duplicate-field", "unknown-policy", "unsupported-policy", "oversized-bindings", "oversized-policy"} {
		t.Run(mode, func(t *testing.T) {
			c, b, storage := fileServiceConfig(t)
			switch mode {
			case "nil-backend":
				c.Backend = nil
			case "typed-nil":
				var nilBackend *fileServiceBackend
				c.Backend = nilBackend
			case "nil-storage":
				c.Storage = nil
			case "no-scope":
				c.Bindings[0].Scope = nil
			case "no-policy":
				c.Bindings[0].Authorize = nil
			case "duplicate-binding":
				c.Bindings = append(c.Bindings, c.Bindings[0])
			case "duplicate-field":
				c.Bindings[0].PolicyFields = []string{"tenant", "tenant"}
			case "unknown-policy":
				c.Bindings[0].PolicyFields = []string{"missing"}
			case "unsupported-policy":
				schema := fileOwnerSchema()
				schema.Fields = append(schema.Fields, models.JSONField("json"))
				r := &models.Registry{}
				if err := r.Register(schema); err != nil {
					t.Fatal(err)
				}
				c.Registry = r
				c.Bindings[0].PolicyFields = []string{"json"}
			case "oversized-bindings":
				c.Bindings = make([]OwnerBinding, 129)
			case "oversized-policy":
				c.Bindings[0].PolicyFields = make([]string, 33)
				b.aliasHook = func() { t.Fatal("provider callback before policy bound") }
			}
			if s, e := NewService(c); s != nil || e != ErrConfiguration {
				t.Fatal(s, e)
			}
			if b.queries != 0 || storage.saves != 0 || b.begins != 0 {
				t.Fatal("constructor performed effects")
			}
		})
	}
	c, _, _ := fileServiceConfig(t)
	s := mustFileService(t, c)
	input := OwnerInput{Binding: "asset", Key: map[string]any{"id": "1"}}
	selected, err := s.state.selectOwner(input)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := s.state.parseOwner(selected.reference)
	if err != nil || parsed.key["id"] != int64(1) {
		t.Fatal(parsed, err)
	}
	for _, bad := range []string{selected.reference + " ", strings.Replace(selected.reference, `"v":1`, `"v":2`, 1), strings.Replace(selected.reference, `"value":"1"`, `"value":"01"`, 1), strings.Repeat("x", MaxOwnerReferenceBytes+1)} {
		if _, err := s.state.parseOwner(bad); err == nil {
			t.Fatal("noncanonical reference accepted")
		}
	}
	for _, err := range []*StoreFailure{nil, {}} {
		for _, verb := range []string{"%s", "%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(verb, err); got != ErrUnavailable.Error() {
				t.Fatal(verb, got)
			}
		}
	}
}

func TestFileServiceScopeUsesOwnReadOnlySnapshot(t *testing.T) {
	c, b, _ := fileServiceConfig(t)
	calls := 0
	c.Bindings[0].Scope = func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		calls++
		if !db.InTransaction(ctx, "files") || schema.Key() != "files_test.Owner" {
			t.Fatal("scope outside own transaction")
		}
		if calls == 1 && (b.tx.options.Isolation != sql.LevelRepeatableRead || !b.tx.options.ReadOnly) {
			t.Fatal(b.tx.options)
		}
		schema.Fields[0].Name = "changed"
		return orm.Q("tenant", "one"), nil
	}
	if _, err := mustFileService(t, c).StoreValidated(context.Background(), fileServiceInput(t), strings.NewReader("data")); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}

func TestFileServiceDetachesTimesBeforeProviderAndPolicyCallbacks(t *testing.T) {
	c, b, storage := fileServiceConfig(t)
	s := mustFileService(t, c)
	first, err := s.StoreValidated(context.Background(), fileServiceInput(t), strings.NewReader("old"))
	if err != nil {
		t.Fatal(err)
	}
	stamp := *first.FinalizedAt
	retained := stamp
	b.files[first.ID][9] = &retained
	b.queryHook = func(tx *fileServiceTx, statement string, args []any) (db.Rows, error, bool) {
		if strings.Contains(statement, `FROM "gogo_files"`) {
			values := append([]any(nil), tx.files[first.ID]...)
			rows := &fileServiceRows{values: [][]any{values}, closeHook: func() { retained = stamp.Add(24 * time.Hour) }}
			return rows, nil, true
		}
		return nil, nil, false
	}
	backend, store := s.state.operation()
	tx, e := backend.BeginTx(context.Background(), db.TxOptions{ReadOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	read, e := compileMetadata(context.Background(), store, orm.Q("id", first.ID), false)
	if e != nil {
		t.Fatal(e)
	}
	file, found, e := read.load(context.Background(), tx)
	if e != nil || !found || !file.FinalizedAt.Equal(stamp) {
		t.Fatal(file, e)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	b.queryHook = nil
	b.files[first.ID][9] = stamp
	var observed []*time.Location
	c.Bindings[0].Authorize = func(_ context.Context, action OwnerAction, owner OwnerSnapshot) error {
		if owner.Previous == nil {
			return nil
		}
		for _, instant := range []time.Time{owner.Previous.CreatedAt, *owner.Previous.FinalizedAt} {
			zone := instant.Location()
			if zone == time.UTC || zone == time.Local {
				t.Fatal("shared global callback location")
			}
			for _, prior := range observed {
				if zone == prior {
					t.Fatal("shared callback location")
				}
			}
			observed = append(observed, zone)
			*zone = *time.FixedZone("mutated", 3600)
		}
		return nil
	}
	s = mustFileService(t, c)
	second, err := s.StoreValidated(context.Background(), fileServiceInput(t), strings.NewReader("new"))
	if err != nil {
		t.Fatal(err)
	}
	if zone := second.CreatedAt.Location(); zone == time.UTC || zone == time.Local {
		t.Fatal("shared global result location")
	}
	*second.CreatedAt.Location() = *time.FixedZone("result changed", 7200)
	opened, r, err := s.Open(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Close() != nil {
		t.Fatal("close")
	}
	_, offset := opened.CreatedAt.Zone()
	if offset != 0 || storage.opens != 1 {
		t.Fatal("result location retargeted state", offset)
	}
}

func TestFileServiceObservedCommitReportsCallbackFailures(t *testing.T) {
	for _, mode := range []string{"error", "panic", "forged-before"} {
		t.Run(mode, func(t *testing.T) {
			c, b, _ := fileServiceConfig(t)
			c.Bindings[0].Authorize = func(ctx context.Context, _ OwnerAction, _ OwnerSnapshot) error {
				if mode == "forged-before" {
					return &db.CommittedCallbackError{Errors: []error{errors.New("forged")}}
				}
				if b.tx.options.ReadOnly {
					return nil
				}
				return db.OnCommit(ctx, "files", func(context.Context) error {
					if mode == "panic" {
						panic("private callback")
					}
					return errors.New("private callback")
				}, false)
			}
			input := fileServiceInput(t)
			info, err := mustFileService(t, c).StoreValidated(context.Background(), input, strings.NewReader("data"))
			var failure *StoreFailure
			if !errors.As(err, &failure) {
				t.Fatal(info, err)
			}
			if mode == "forged-before" {
				if info != (Info{}) || failure.Committed || failure.Published || b.writes != 0 || !errors.Is(err, ErrUnavailable) {
					t.Fatal(info, err)
				}
				return
			}
			if !failure.Committed || !failure.Published || !errors.Is(err, ErrCommittedCallback) || info.ID != input.Identity.ID || b.owner[2] != input.Identity.Key {
				t.Fatal(info, err, failure)
			}
			if strings.Contains(fmt.Sprintf("%#v", err), "private") {
				t.Fatal("callback error leaked")
			}
		})
	}
}

type fileServiceContext struct {
	context.Context
	hook func()
}

func (c *fileServiceContext) Err() error {
	if c.hook != nil {
		hook := c.hook
		c.hook = nil
		hook()
	}
	return c.Context.Err()
}

func TestFileServiceFreezesConstructorAndContextInputs(t *testing.T) {
	c, b, _ := fileServiceConfig(t)
	calls := 0
	c.Bindings[0].Authorize = func(context.Context, OwnerAction, OwnerSnapshot) error { calls++; return nil }
	b.aliasHook = func() {
		c.Bindings[0].PolicyFields[0] = "missing"
		c.Bindings[0].Authorize = func(context.Context, OwnerAction, OwnerSnapshot) error { panic("replacement policy") }
		*c.Registry = models.Registry{}
	}
	s := mustFileService(t, c)
	input := fileServiceInput(t)
	identity := input.Identity
	ctx := &fileServiceContext{Context: context.Background(), hook: func() { input.Owner.Key["id"] = int64(9); *s = Service{} }}
	info, err := s.StoreValidated(ctx, input, strings.NewReader("data"))
	if err != nil || info.ID != identity.ID || calls != 2 || b.owner[2] != identity.Key {
		t.Fatal(info, err, calls)
	}
}
