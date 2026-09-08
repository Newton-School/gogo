package redirects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type redirectDialect struct{}

func (redirectDialect) Name() string                             { return "postgres" }
func (redirectDialect) QuoteIdentifier(s string) (string, error) { return `"` + s + `"`, nil }
func (redirectDialect) Placeholder(i int) string                 { return fmt.Sprintf("$%d", i) }
func (redirectDialect) FieldType(models.Field) (string, error)   { return "text", nil }

type redirectBackend struct {
	db.Backend
	alias                    string
	tx                       *redirectTx
	begin                    func(context.Context, db.TxOptions) (db.Transaction, error)
	beginCalls, outsideCalls int
}

func (b *redirectBackend) Alias() string {
	if b.alias == "" {
		return "redirects"
	}
	return b.alias
}
func (*redirectBackend) Dialect() db.Dialect { return redirectDialect{} }
func (b *redirectBackend) BeginTx(ctx context.Context, o db.TxOptions) (db.Transaction, error) {
	b.beginCalls++
	if b.begin != nil {
		return b.begin(ctx, o)
	}
	return b.tx, nil
}
func (b *redirectBackend) Query(context.Context, string, ...any) (db.Rows, error) {
	b.outsideCalls++
	return nil, errors.New("outside query")
}

type redirectTx struct {
	db.Transaction
	query                       func(context.Context, string, []any) (db.Rows, error)
	commitHook, rollbackHook    func() error
	queries, commits, rollbacks int
}

func (tx *redirectTx) Query(ctx context.Context, s string, args ...any) (db.Rows, error) {
	tx.queries++
	return tx.query(ctx, s, args)
}
func (tx *redirectTx) Commit() error {
	tx.commits++
	if tx.commitHook != nil {
		return tx.commitHook()
	}
	return nil
}
func (tx *redirectTx) Rollback() error {
	tx.rollbacks++
	if tx.rollbackHook != nil {
		return tx.rollbackHook()
	}
	return nil
}

type redirectRows struct {
	db.Rows
	values                 [][]any
	i, closes              int
	err, closeErr, scanErr error
	closeHook              func()
}

func (r *redirectRows) Next() bool {
	if r.i == len(r.values) {
		return false
	}
	r.i++
	return true
}
func (r *redirectRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	for i, v := range r.values[r.i-1] {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *bool:
			*d = v.(bool)
		default:
			panic("unexpected scan")
		}
	}
	return nil
}
func (r *redirectRows) Err() error { return r.err }
func (r *redirectRows) Close() error {
	r.closes++
	if r.closeHook != nil {
		r.closeHook()
	}
	return r.closeErr
}

func redirectFixture(entries map[string]string) (*redirectBackend, *redirectTx) {
	ids := map[string]string{}
	i := 10
	for key := range entries {
		ids[key] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		i++
	}
	tx := &redirectTx{}
	tx.query = func(ctx context.Context, query string, args []any) (db.Rows, error) {
		if strings.Contains(query, `FROM "gogo_sites"`) {
			return &redirectRows{values: [][]any{{redirectSiteID, "example.test", "Example", true}}}, nil
		}
		if len(args) != 3 || args[0] != redirectSiteID || args[2] != 2 || !strings.Contains(query, "LIMIT $3") {
			panic("unbounded/unscoped redirect query")
		}
		key := args[1].(string)
		if target, ok := entries[key]; ok {
			return &redirectRows{values: [][]any{{ids[key], redirectSiteID, key, target, true}}}, nil
		}
		return &redirectRows{}, nil
	}
	return &redirectBackend{tx: tx}, tx
}

func TestRedirectLookupFirstHopAndEffectiveChains(t *testing.T) {
	for _, test := range []struct {
		name, uri       string
		entries         map[string]string
		slash, preserve bool
		limit           int
		location        string
		found, external bool
		want            error
	}{
		{name: "missing", uri: "/a", entries: map[string]string{}},
		{name: "first hop", uri: "/a", entries: map[string]string{"/a": "/b", "/b": "/c"}, location: "/b", found: true},
		{name: "first gone", uri: "/a", entries: map[string]string{"/a": ""}, found: true},
		{name: "later gone", uri: "/a", entries: map[string]string{"/a": "/b", "/b": ""}, location: "/b", found: true},
		{name: "slash", uri: "/a?q=1", entries: map[string]string{"/a/?q=1": "/b"}, slash: true, preserve: true, location: "/b?q=1", found: true},
		{name: "exact wins", uri: "/a", entries: map[string]string{"/a": "/b", "/a/": "/c"}, slash: true, location: "/b", found: true},
		{name: "query isolated", uri: "/a?x=1", entries: map[string]string{"/a": "/b"}},
		{name: "external", uri: "/a", entries: map[string]string{"/a": "https://other.test/end"}, location: "https://other.test/end", found: true, external: true},
		{name: "same origin", uri: "/a", entries: map[string]string{"/a": "https://example.test/b", "/b": "/a"}, want: ErrCycle},
		{name: "cycle", uri: "/a", entries: map[string]string{"/a": "/b", "/b": "/a"}, want: ErrCycle},
		{name: "slash cycle", uri: "/a", entries: map[string]string{"/a/": "/a"}, slash: true, want: ErrCycle},
		{name: "preserved query cycle", uri: "/a?x=1", entries: map[string]string{"/a?x=1": "/b", "/b?x=1": "/a"}, preserve: true, want: ErrCycle},
		{name: "malformed later", uri: "/a", entries: map[string]string{"/a": "/b", "/b": "javascript:bad"}, want: ErrInvalid},
		{name: "exact hop bound", uri: "/a", entries: map[string]string{"/a": "/b"}, limit: 1, location: "/b", found: true},
		{name: "hop overflow", uri: "/a", entries: map[string]string{"/a": "/b", "/b": "/c"}, limit: 1, want: ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend, tx := redirectFixture(test.entries)
			backend.begin = func(_ context.Context, options db.TxOptions) (db.Transaction, error) {
				if options.Isolation != sql.LevelRepeatableRead || !options.ReadOnly {
					t.Fatal(options)
				}
				return tx, nil
			}
			r, err := New(Config{Backend: backend, AppendSlash: test.slash, PreserveQuery: test.preserve, MaxHops: test.limit})
			if err != nil {
				t.Fatal(err)
			}
			match, found, err := r.Lookup(context.Background(), LookupInput{SiteID: redirectSiteID, URI: test.uri, Origin: "https://example.test"})
			if !errors.Is(err, test.want) || found != test.found || match.Location != test.location || match.External != test.external {
				t.Fatal(match, found, err)
			}
			if err != nil && match != (Match{}) {
				t.Fatal("partial result", match)
			}
			if backend.outsideCalls != 0 || backend.beginCalls != 1 || tx.queries > 1+2*(MaxHops+1) {
				t.Fatal("transaction/bounds", backend, tx)
			}
			if err == nil && tx.commits != 1 || err != nil && tx.rollbacks != 1 {
				t.Fatal("outcome", tx)
			}
		})
	}
}

func TestRedirectLookupProviderFailuresAreEmpty(t *testing.T) {
	for _, stage := range []string{"site-missing", "site-inactive", "site-duplicate", "site-id", "redirect-duplicate", "redirect-id", "redirect-site", "redirect-path", "query", "nil-rows", "typed-nil-rows", "scan", "rows-err", "close", "panic", "nil-tx", "typed-nil-tx", "begin", "partial-begin", "commit", "rollback", "cancel-close", "cancel-commit"} {
		t.Run(stage, func(t *testing.T) {
			backend, tx := redirectFixture(map[string]string{"/a": ""})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := tx.query
			want := ErrUnavailable
			tx.query = func(ctx context.Context, query string, args []any) (db.Rows, error) {
				base, err := original(ctx, query, args)
				if err != nil {
					return nil, err
				}
				rows := base.(*redirectRows)
				if strings.Contains(query, `FROM "gogo_sites"`) {
					switch stage {
					case "site-missing":
						rows.values = nil
						want = ErrSiteNotConfigured
					case "site-inactive":
						rows.values[0][3] = false
						want = ErrSiteNotConfigured
					case "site-duplicate":
						rows.values = append(rows.values, rows.values[0])
					case "site-id":
						rows.values[0][0] = redirectOtherID
					}
					return rows, nil
				}
				switch stage {
				case "redirect-duplicate":
					rows.values = append(rows.values, rows.values[0])
				case "redirect-id":
					rows.values[0][0] = "bad"
					want = ErrInvalid
				case "redirect-site":
					rows.values[0][1] = redirectOtherID
					want = ErrInvalid
				case "redirect-path":
					rows.values[0][2] = "/different"
					want = ErrInvalid
				case "query":
					return nil, errors.New("private query details")
				case "nil-rows":
					return nil, nil
				case "typed-nil-rows":
					return (*redirectRows)(nil), nil
				case "scan":
					rows.scanErr = errors.New("scan")
				case "rows-err":
					rows.err = errors.New("rows")
				case "close":
					rows.closeErr = errors.New("close")
				case "panic":
					panic("private panic")
				case "rollback":
					rows.err = errors.New("rows")
					tx.rollbackHook = func() error { return errors.New("rollback") }
				case "cancel-close":
					rows.closeHook = cancel
					want = context.Canceled
				}
				return rows, nil
			}
			switch stage {
			case "nil-tx":
				backend.begin = func(context.Context, db.TxOptions) (db.Transaction, error) { return nil, nil }
			case "typed-nil-tx":
				backend.begin = func(context.Context, db.TxOptions) (db.Transaction, error) { return (*redirectTx)(nil), nil }
			case "begin":
				backend.begin = func(context.Context, db.TxOptions) (db.Transaction, error) { return nil, errors.New("begin") }
			case "partial-begin":
				backend.begin = func(context.Context, db.TxOptions) (db.Transaction, error) { return tx, errors.New("partial begin") }
			case "commit":
				tx.commitHook = func() error { return errors.New("private commit") }
			case "cancel-commit":
				tx.commitHook = func() error { cancel(); return nil }
				want = context.Canceled
			}
			r, _ := New(Config{Backend: backend})
			match, found, err := r.Lookup(ctx, LookupInput{SiteID: redirectSiteID, URI: "/a", Origin: "https://example.test"})
			if !errors.Is(err, want) || found || match != (Match{}) {
				t.Fatal(match, found, err, want)
			}
			if stage == "partial-begin" && tx.rollbacks != 1 {
				t.Fatal("partial transaction leaked", tx.rollbacks)
			}
		})
	}
}

type lookupMutationContext struct {
	context.Context
	mutate func()
}

func (c *lookupMutationContext) Err() error {
	if c.mutate != nil {
		f := c.mutate
		c.mutate = nil
		f()
	}
	return c.Context.Err()
}

func TestRedirectLookupEntrySnapshotAndAmbientTransactions(t *testing.T) {
	backend, tx := redirectFixture(map[string]string{"/a/": ""})
	r, _ := New(Config{Backend: backend, AppendSlash: true})
	ctx := &lookupMutationContext{Context: context.Background(), mutate: func() { *r = Resolver{}; backend.alias = "changed" }}
	if match, found, err := r.Lookup(ctx, LookupInput{SiteID: redirectSiteID, URI: "/a", Origin: "https://example.test"}); err != nil || !found || match.OldPath != "/a/" {
		t.Fatal(match, found, err)
	}
	if tx.commits != 1 {
		t.Fatal(tx)
	}
	if _, _, err := r.Lookup(context.Background(), LookupInput{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	backend, _ = redirectFixture(map[string]string{})
	r, _ = New(Config{Backend: backend})
	if err := db.Atomic(context.Background(), backend, db.AtomicOptions{}, func(ctx context.Context) error {
		match, found, err := r.Lookup(ctx, LookupInput{SiteID: redirectSiteID, URI: "/a", Origin: "https://example.test"})
		if !errors.Is(err, ErrTransaction) || found || match != (Match{}) || backend.beginCalls != 1 {
			t.Fatal(match, found, err, backend.beginCalls)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	other, otherTx := redirectFixture(nil)
	other.alias = "other"
	if err := db.Atomic(context.Background(), other, db.AtomicOptions{}, func(ctx context.Context) error {
		if _, _, err := r.Lookup(ctx, LookupInput{SiteID: redirectSiteID, URI: "/a", Origin: "https://example.test"}); err != nil {
			t.Fatal(err)
		}
		if !db.InTransaction(ctx, "other") || otherTx.commits != 0 || otherTx.rollbacks != 0 {
			t.Fatal("ambient changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, config := range []Config{{}, {Backend: (*redirectBackend)(nil)}, {Backend: backend, MaxHops: -1}, {Backend: backend, MaxHops: MaxHops + 1}} {
		if _, err := New(config); !errors.Is(err, ErrConfiguration) {
			t.Fatal(config, err)
		}
	}
}
