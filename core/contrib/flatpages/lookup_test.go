package flatpages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type flatDialect struct{}

func (flatDialect) Name() string                           { return "test" }
func (flatDialect) Placeholder(i int) string               { return fmt.Sprintf("$%d", i) }
func (flatDialect) FieldType(models.Field) (string, error) { return "text", nil }
func (flatDialect) QuoteIdentifier(s string) (string, error) {
	if !models.ValidIdentifier(s) {
		return "", ErrInvalid
	}
	return `"` + s + `"`, nil
}

type flatBackend struct {
	db.Backend
	pages           map[string]Info
	links           []linkInfo
	sites           map[string]siteInfo
	tx              *flatTx
	beginHook       func(context.Context, db.TxOptions) (db.Transaction, error)
	queryHook       func(*flatTx, string, []any) (db.Rows, error, bool)
	commitHook      func() error
	alias           string
	begins, outside int
}

func (b *flatBackend) Alias() string {
	if b.alias != "" {
		return b.alias
	}
	return "flat"
}
func (*flatBackend) Dialect() db.Dialect { return flatDialect{} }
func (*flatBackend) Capabilities() db.Capabilities {
	return db.Capabilities{"transactions": true, "row_locks": true, "update_matched_rows": true}
}
func (b *flatBackend) Query(context.Context, string, ...any) (db.Rows, error) {
	b.outside++
	return nil, ErrUnavailable
}
func (b *flatBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	b.begins++
	if b.beginHook != nil {
		return b.beginHook(ctx, options)
	}
	tx := &flatTx{backend: b, pages: map[string]Info{}, links: slices.Clone(b.links), sites: map[string]siteInfo{}, options: options}
	for id, page := range b.pages {
		tx.pages[id] = page
	}
	for id, site := range b.sites {
		tx.sites[id] = site
	}
	b.tx = tx
	return tx, nil
}

type flatTx struct {
	db.Transaction
	backend                             *flatBackend
	pages                               map[string]Info
	links                               []linkInfo
	sites                               map[string]siteInfo
	options                             db.TxOptions
	queries, writes, commits, rollbacks int
}

func (tx *flatTx) Query(_ context.Context, statement string, args ...any) (db.Rows, error) {
	tx.queries++
	if tx.backend.queryHook != nil {
		if rows, err, handled := tx.backend.queryHook(tx, statement, args); handled {
			return rows, err
		}
	}
	rows := &flatRows{}
	if strings.HasPrefix(statement, `INSERT INTO "gogo_flatpages"`) {
		tx.writes++
		page := Info{ID: args[0].(string), URL: args[1].(string), Title: args[2].(string), Content: args[3].(string), TemplateName: args[4].(string), RegistrationRequired: args[5].(bool)}
		tx.pages[page.ID] = page
		rows.values = append(rows.values, pageValues(page))
		return rows, nil
	}
	if strings.Contains(statement, `FROM "gogo_sites"`) {
		if site, ok := tx.sites[args[0].(string)]; ok {
			rows.values = append(rows.values, []any{site.ID, site.Domain, site.DisplayName, site.Active})
		}
		return rows, nil
	}
	if strings.Contains(statement, `FROM "gogo_flatpages"`) {
		if page, ok := tx.pages[args[0].(string)]; ok {
			rows.values = append(rows.values, pageValues(page))
		}
		return rows, nil
	}
	if strings.Contains(statement, `FROM "gogo_flatpage_sites"`) {
		for _, link := range tx.links {
			match := false
			if strings.Contains(statement, `"page_id" = $1`) {
				match = link.PageID == args[0]
			} else {
				match = link.SiteID == args[0] && link.URL == args[1]
			}
			if match {
				rows.values = append(rows.values, []any{link.ID, link.PageID, link.SiteID, link.URL})
			}
		}
		return rows, nil
	}
	return nil, errors.New("unexpected fixture statement")
}
func (tx *flatTx) Commit() error {
	tx.commits++
	if tx.backend.commitHook != nil {
		if err := tx.backend.commitHook(); err != nil {
			return err
		}
	}
	tx.backend.pages, tx.backend.links, tx.backend.sites = tx.pages, tx.links, tx.sites
	return nil
}
func (tx *flatTx) Rollback() error { tx.rollbacks++; return nil }
func pageValues(p Info) []any {
	return []any{p.ID, p.URL, p.Title, p.Content, p.TemplateName, p.RegistrationRequired}
}

type flatRows struct {
	db.Rows
	values                 [][]any
	i, closes              int
	err, closeErr, scanErr error
	closeHook              func()
}

func (r *flatRows) Next() bool {
	if r.i >= len(r.values) {
		return false
	}
	r.i++
	return true
}
func (r *flatRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	for i, value := range r.values[r.i-1] {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *bool:
			*target = value.(bool)
		case *any:
			*target = value
		default:
			panic("unexpected destination")
		}
	}
	return nil
}
func (r *flatRows) Err() error { return r.err }
func (r *flatRows) Close() error {
	r.closes++
	if r.closeHook != nil {
		r.closeHook()
	}
	return r.closeErr
}
func flatFixture() *flatBackend {
	return &flatBackend{pages: map[string]Info{flatID: {ID: flatID, URL: "/page/", Title: "Page", Content: "private body", RegistrationRequired: true}}, links: []linkInfo{{ID: "00000000-0000-4000-8000-000000000100", PageID: flatID, SiteID: flatSiteA, URL: "/page/"}}, sites: map[string]siteInfo{flatSiteA: {ID: flatSiteA, Domain: "example.test", DisplayName: "Example", Active: true}, flatSiteB: {ID: flatSiteB, Domain: "second.test", DisplayName: "Second", Active: true}, flatSiteC: {ID: flatSiteC, Domain: "third.test", DisplayName: "Third", Active: true}}}
}

func TestFlatPageLookupSnapshotAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"success", "missing", "inactive", "mapping-drift", "page-drift", "duplicate", "query", "nil-rows", "typed-nil", "close", "scan", "rows-error", "panic", "commit", "cancel-commit"} {
		t.Run(mode, func(t *testing.T) {
			b := flatFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := ErrUnavailable
			switch mode {
			case "success":
				want = nil
			case "missing":
				b.links = nil
				want = nil
			case "inactive":
				site := b.sites[flatSiteA]
				site.Active = false
				b.sites[flatSiteA] = site
				want = ErrSiteNotConfigured
			case "mapping-drift":
				b.links[0].PageID = flatSiteB
			case "page-drift":
				p := b.pages[flatID]
				p.URL = "/different/"
				b.pages[flatID] = p
			case "duplicate":
				b.links = append(b.links, b.links[0])
			case "commit":
				b.commitHook = func() error { return errors.New("private commit") }
			case "cancel-commit":
				b.commitHook = func() error { cancel(); return nil }
				want = context.Canceled
			}
			b.queryHook = func(tx *flatTx, s string, args []any) (db.Rows, error, bool) {
				if !strings.Contains(s, `FROM "gogo_flatpages"`) {
					return nil, nil, false
				}
				rows := &flatRows{values: [][]any{pageValues(tx.pages[flatID])}}
				switch mode {
				case "query":
					return nil, errors.New("private query"), true
				case "nil-rows":
					return nil, nil, true
				case "typed-nil":
					return (*flatRows)(nil), nil, true
				case "close":
					rows.closeErr = ErrUnavailable
				case "scan":
					rows.scanErr = ErrUnavailable
				case "rows-error":
					rows.err = ErrUnavailable
				case "panic":
					panic("provider")
				default:
					return nil, nil, false
				}
				return rows, nil, true
			}
			store, err := New(Config{Backend: b})
			if err != nil {
				t.Fatal(err)
			}
			info, found, err := store.Lookup(ctx, LookupInput{SiteID: flatSiteA, URL: "/page/"})
			if !errors.Is(err, want) || found != (mode == "success") {
				t.Fatal(info, found, err, want)
			}
			if err != nil && info != (Info{}) {
				t.Fatal("partial private content", info)
			}
			if b.outside != 0 || b.tx.options.Isolation != sql.LevelRepeatableRead || !b.tx.options.ReadOnly {
				t.Fatal("snapshot mode", b.tx.options, b.outside)
			}
		})
	}
}
