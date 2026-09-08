package postgres_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/contrib/flatpages"
	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func setupFlatpages(t *testing.T) (*postgres.Backend, *orm.Store, []*sites.Site, *migrations.Executor) {
	t.Helper()
	b := openTest(t)
	ctx := context.Background()
	runner := &migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: append(sites.Migrations(), flatpages.Migrations()...)}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{(&sites.Site{}).Schema(), (&flatpages.FlatPage{}).Schema(), (&flatpages.FlatPageSite{}).Schema()} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	var all []*sites.Site
	for _, domain := range []string{"example.test", "second.test", "third.test"} {
		site, err := sites.NewSite(domain, domain)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(ctx, site, orm.SaveOptions{ForceInsert: true, Prepare: sites.Prepare}); err != nil {
			t.Fatal(err)
		}
		all = append(all, site)
	}
	return b, store, all, runner
}
func newFlatStore(t *testing.T, b db.Backend, authorize func(context.Context, flatpages.Change) error) *flatpages.Store {
	t.Helper()
	store, err := flatpages.New(flatpages.Config{Backend: b, AuthorizeChange: authorize})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func allowFlatChange(context.Context, flatpages.Change) error { return nil }

func TestPostgresFlatpagesSharedURLReplacementAndConstraints(t *testing.T) {
	b, ormStore, all, runner := setupFlatpages(t)
	ctx := context.Background()
	store := newFlatStore(t, b, allowFlatChange)
	saved, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/shared/%7e/", Title: "Shared", Content: "Initial", RegistrationRequired: true}, SiteIDs: []string{all[1].ID, all[0].ID}})
	if err != nil || saved.Page.URL != "/shared/~/" || len(saved.SiteIDs) != 2 {
		t.Fatal(saved, err)
	}
	if result, err := store.SaveDomain(ctx, flatpages.SaveInput{Create: true, Page: flatpages.Draft{ID: saved.Page.ID, URL: "/must-not-upsert/", Title: "Conflict"}}); err != flatpages.ErrConflict || result.Page != (flatpages.Info{}) {
		t.Fatal("create-only replaced existing identity", result, err)
	}
	for _, site := range all[:2] {
		info, found, err := store.Lookup(ctx, flatpages.LookupInput{SiteID: site.ID, URL: "/shared/~/"})
		if err != nil || !found || info != saved.Page {
			t.Fatal(info, found, err)
		}
	}
	if info, found, err := store.Lookup(ctx, flatpages.LookupInput{SiteID: all[2].ID, URL: "/shared/~/"}); err != nil || found || info != (flatpages.Info{}) {
		t.Fatal(info, found, err)
	}
	// The same URL can belong to another page on a disjoint site.
	other, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: saved.Page.URL, Title: "Other"}, SiteIDs: []string{all[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: saved.Page.URL, Title: "Conflict"}, SiteIDs: []string{all[0].ID}}); !errors.Is(err, flatpages.ErrConflict) || result.Page != (flatpages.Info{}) {
		t.Fatal(result, err)
	}
	if count, err := orm.For(ormStore, func() *flatpages.FlatPage { return &flatpages.FlatPage{} }).Count(ctx); err != nil || count != 2 {
		t.Fatal("conflicting page leaked", count, err)
	}
	previousLinks, err := orm.For(ormStore, func() *flatpages.FlatPageSite { return &flatpages.FlatPageSite{} }).Filter(orm.Q("page_id", saved.Page.ID)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var retainedID string
	for _, link := range previousLinks {
		if link.SiteID == all[0].ID {
			retainedID = link.ID
		}
	}
	renamed, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{ID: saved.Page.ID, URL: "/renamed/", Title: "Renamed", Content: "Updated"}, SiteIDs: []string{all[2].ID, all[0].ID}})
	if err != nil || renamed.Page.ID != saved.Page.ID {
		t.Fatal(renamed, err)
	}
	for _, site := range all {
		info, found, err := store.Lookup(ctx, flatpages.LookupInput{SiteID: site.ID, URL: "/renamed/"})
		want := site.ID != all[1].ID
		if err != nil || found != want || found && info != renamed.Page {
			t.Fatal(site.ID, info, found, err)
		}
	}
	links, err := orm.For(ormStore, func() *flatpages.FlatPageSite { return &flatpages.FlatPageSite{} }).Filter(orm.Q("page_id", saved.Page.ID)).All(ctx)
	if err != nil || len(links) != 2 {
		t.Fatal(links, err)
	}
	for _, link := range links {
		if link.URL != "/renamed/" || link.SiteID == all[0].ID && link.ID != retainedID {
			t.Fatal("inconsistent or replaced retained mapping", link)
		}
	}
	// Real unique constraints protect both representations independently of API.
	duplicate := &flatpages.FlatPageSite{ID: "00000000-0000-4000-8000-000000000999", PageID: other.Page.ID, SiteID: all[2].ID, URL: "/different/"}
	if err := ormStore.Save(ctx, duplicate, orm.SaveOptions{ForceInsert: true}); !db.IsCode(err, db.UniqueViolation) {
		t.Fatal("pair uniqueness absent", err)
	}
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(txctx context.Context) error {
		if _, err := store.SaveDomain(txctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/nested/", Title: "Nested"}}); err != flatpages.ErrTransaction {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ormStore.Delete(ctx, all[0]); err != nil {
		t.Fatal(err)
	}
	if count, err := orm.For(ormStore, func() *flatpages.FlatPageSite { return &flatpages.FlatPageSite{} }).Filter(orm.Q("page_id", saved.Page.ID)).Count(ctx); err != nil || count != 1 {
		t.Fatal("site deletion did not cascade mapping", count, err)
	}
	detached, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{ID: saved.Page.ID, URL: "/renamed/", Title: "Detached"}})
	if err != nil || len(detached.SiteIDs) != 0 {
		t.Fatal(detached, err)
	}
	if err := runner.Reverse(ctx, "gogo_flatpages.zero"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresFlatpagesRefusesPrivilegedURLDriftAndInactiveSite(t *testing.T) {
	b, _, all, _ := setupFlatpages(t)
	ctx := context.Background()
	store := newFlatStore(t, b, allowFlatChange)
	saved, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/page/", Title: "Page"}, SiteIDs: []string{all[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctx, `UPDATE "gogo_flatpage_sites" SET url='/drift/' WHERE page_id=$1`, saved.Page.ID); err != nil {
		t.Fatal(err)
	}
	if info, found, err := store.Lookup(ctx, flatpages.LookupInput{SiteID: all[0].ID, URL: "/drift/"}); err != flatpages.ErrUnavailable || found || info != (flatpages.Info{}) {
		t.Fatal(info, found, err)
	}
	if result, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{ID: saved.Page.ID, URL: "/repair/", Title: "No implicit repair"}, SiteIDs: []string{all[0].ID}}); err != flatpages.ErrUnavailable || result.Page != (flatpages.Info{}) {
		t.Fatal(result, err)
	}
	if _, err := b.Exec(ctx, `UPDATE "gogo_flatpage_sites" SET url='/page/' WHERE page_id=$1`, saved.Page.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctx, `UPDATE "gogo_sites" SET active=false WHERE id=$1`, all[0].ID); err != nil {
		t.Fatal(err)
	}
	if info, found, err := store.Lookup(ctx, flatpages.LookupInput{SiteID: all[0].ID, URL: "/page/"}); err != flatpages.ErrSiteNotConfigured || found || info != (flatpages.Info{}) {
		t.Fatal(info, found, err)
	}
}

type flatSnapshotBackend struct {
	db.Backend
	t         *testing.T
	afterSite func()
	options   []db.TxOptions
}

func (b *flatSnapshotBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	b.options = append(b.options, options)
	var isolation, readonly string
	if err := db.QueryRow(ctx, tx, "SELECT current_setting('transaction_isolation'),current_setting('transaction_read_only')", nil, &isolation, &readonly); err != nil {
		tx.Rollback()
		return nil, err
	}
	if isolation != "repeatable read" || readonly != "on" {
		b.t.Fatal(isolation, readonly)
	}
	return &flatSnapshotTx{Transaction: tx, owner: b}, nil
}

type flatSnapshotTx struct {
	db.Transaction
	owner *flatSnapshotBackend
}

func (tx *flatSnapshotTx) Query(ctx context.Context, q string, args ...any) (db.Rows, error) {
	rows, err := tx.Transaction.Query(ctx, q, args...)
	if err == nil && strings.Contains(q, `FROM "gogo_sites"`) && tx.owner.afterSite != nil {
		f := tx.owner.afterSite
		tx.owner.afterSite = nil
		f()
	}
	return rows, err
}

func TestPostgresFlatpagesLookupUsesOneCommittedSnapshot(t *testing.T) {
	b, _, all, _ := setupFlatpages(t)
	ctx := context.Background()
	writer := newFlatStore(t, b, allowFlatChange)
	saved, err := writer.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/before/", Title: "Before", Content: "Original"}, SiteIDs: []string{all[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &flatSnapshotBackend{Backend: b, t: t}
	wrapped.afterSite = func() {
		if _, err := writer.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{ID: saved.Page.ID, URL: "/after/", Title: "After", Content: "Changed"}, SiteIDs: []string{all[0].ID}}); err != nil {
			t.Fatal(err)
		}
	}
	reader := newFlatStore(t, wrapped, nil)
	info, found, err := reader.Lookup(ctx, flatpages.LookupInput{SiteID: all[0].ID, URL: "/before/"})
	if err != nil || !found || info != saved.Page {
		t.Fatal(info, found, err)
	}
	if _, found, err := reader.Lookup(ctx, flatpages.LookupInput{SiteID: all[0].ID, URL: "/before/"}); err != nil || found {
		t.Fatal(found, err)
	}
	if info, found, err := reader.Lookup(ctx, flatpages.LookupInput{SiteID: all[0].ID, URL: "/after/"}); err != nil || !found || info.Content != "Changed" {
		t.Fatal(info, found, err)
	}
	if !slices.IsSorted(saved.SiteIDs) {
		t.Fatal("noncanonical result")
	}
}

func TestPostgresFlatpagesConcurrentCallerIdentityNeverUpserts(t *testing.T) {
	b, ormStore, _, _ := setupFlatpages(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	store := newFlatStore(t, b, func(ctx context.Context, change flatpages.Change) error {
		if change.Action != flatpages.CreatePage {
			return flatpages.ErrForbidden
		}
		arrived <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	type outcome struct {
		saved flatpages.Saved
		err   error
	}
	results := make(chan outcome, 2)
	id := "00000000-0000-4000-8000-000000000025"
	for _, title := range []string{"First", "Second"} {
		go func(title string) {
			saved, err := store.SaveDomain(ctx, flatpages.SaveInput{Create: true, Page: flatpages.Draft{ID: id, URL: "/identity/", Title: title}})
			results <- outcome{saved, err}
		}(title)
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("concurrent create did not reach grants", ctx.Err())
		}
	}
	close(release)
	winners := 0
	var winningTitle string
	for range 2 {
		select {
		case result := <-results:
			if result.err == nil {
				winners++
				winningTitle = result.saved.Page.Title
				if result.saved.Page.ID != id {
					t.Fatal(result)
				}
			} else if result.err != flatpages.ErrConflict && result.err != flatpages.ErrUnavailable {
				t.Fatal("known PK conflict became uncertain/success", result)
			} else if result.saved.Page != (flatpages.Info{}) {
				t.Fatal("partial losing result", result)
			}
		case <-ctx.Done():
			t.Fatal("concurrent create failed to complete", ctx.Err())
		}
	}
	if winners != 1 {
		t.Fatal("create-only winner count", winners)
	}
	page, err := orm.For(ormStore, func() *flatpages.FlatPage { return &flatpages.FlatPage{} }).Filter(orm.Q("id", id)).Get(ctx)
	if err != nil || page.Title != winningTitle {
		t.Fatal("loser overwrote winner", page, err, winningTitle)
	}
	if count, err := orm.For(ormStore, func() *flatpages.FlatPage { return &flatpages.FlatPage{} }).Count(ctx); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

type flatOutcomeBackend struct {
	db.Backend
	afterCommit func() error
}

func (b flatOutcomeBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return flatOutcomeTx{Transaction: tx, afterCommit: b.afterCommit}, nil
}

type flatOutcomeTx struct {
	db.Transaction
	afterCommit func() error
}

func (tx flatOutcomeTx) Commit() error {
	if err := tx.Transaction.Commit(); err != nil {
		return err
	}
	return tx.afterCommit()
}

func TestPostgresFlatpagesReconcileActualCommitWithUncertainAcknowledgement(t *testing.T) {
	for _, mode := range []string{"unknown acknowledgement", "known success after cancellation"} {
		t.Run(mode, func(t *testing.T) {
			b, ormStore, all, _ := setupFlatpages(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wrapped := flatOutcomeBackend{Backend: b, afterCommit: func() error {
				if mode == "unknown acknowledgement" {
					return &db.Error{Code: db.UnknownCommit}
				}
				cancel()
				return nil
			}}
			store := newFlatStore(t, wrapped, allowFlatChange)
			id := "00000000-0000-4000-8000-000000000026"
			input := flatpages.SaveInput{Create: true, Page: flatpages.Draft{ID: id, URL: "/committed/", Title: "Committed"}, SiteIDs: []string{all[1].ID, all[0].ID}}
			saved, err := store.SaveDomain(ctx, input)
			if mode == "unknown acknowledgement" {
				if err != flatpages.ErrOutcomeUnknown || saved.Page != (flatpages.Info{}) || len(saved.SiteIDs) != 0 {
					t.Fatal(saved, err)
				}
			} else if err != nil || saved.Page.ID != id || ctx.Err() != context.Canceled {
				t.Fatal("known commit was replaced by cancellation", saved, err, ctx.Err())
			}
			// This wrapper proves committed storage plus a synthetic lost-ack
			// signal. It does not simulate or claim a real network failure.
			page, err := orm.For(ormStore, func() *flatpages.FlatPage { return &flatpages.FlatPage{} }).Filter(orm.Q("id", id)).Get(context.Background())
			if err != nil || page.URL != input.Page.URL || page.Title != input.Page.Title {
				t.Fatal("cannot reconcile known caller identity", page, err)
			}
			links, err := orm.For(ormStore, func() *flatpages.FlatPageSite { return &flatpages.FlatPageSite{} }).Filter(orm.Q("page_id", id)).All(context.Background())
			if err != nil || len(links) != 2 {
				t.Fatal(links, err)
			}
			for _, link := range links {
				if link.URL != page.URL || !slices.Contains(input.SiteIDs, link.SiteID) {
					t.Fatal("partial committed association", link)
				}
			}
			if input.SiteIDs[0] != all[1].ID {
				t.Fatal("caller input mutated")
			}
		})
	}
}
