package flatpages

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
)

func TestFlatPageSaveDomainCommitOutcomes(t *testing.T) {
	conflict := &db.Error{Code: db.UniqueViolation, Constraint: SiteURLConstraint}
	unknown := &db.Error{Code: db.UnknownCommit}
	for _, mode := range []string{"success", "late-cancel-success", "known-conflict", "unknown", "mixed", "cause", "panic", "cancel-before"} {
		t.Run(mode, func(t *testing.T) {
			b := flatFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := ErrOutcomeUnknown
			switch mode {
			case "success":
				want = nil
			case "late-cancel-success":
				b.commitHook = func() error { cancel(); return nil }
				want = nil
			case "known-conflict":
				b.commitHook = func() error { return conflict }
				want = ErrConflict
			case "unknown":
				b.commitHook = func() error { return unknown }
			case "mixed":
				b.commitHook = func() error { return errors.Join(conflict, unknown) }
			case "cause":
				b.commitHook = func() error {
					return &db.Error{Code: db.UniqueViolation, Constraint: SiteURLConstraint, Cause: unknown}
				}
			case "panic":
				b.commitHook = func() error { panic("commit failed") }
			case "cancel-before":
				want = context.Canceled
			}
			calls := 0
			store, err := New(Config{Backend: b, AuthorizeChange: func(_ context.Context, change Change) error {
				calls++
				if change.Action != CreatePage || change.Before != nil || change.SiteID != "" || change.After.ID == "" {
					t.Fatal(change)
				}
				if mode == "cancel-before" {
					cancel()
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.SaveDomain(ctx, SaveInput{Page: Draft{URL: "/created/", Title: "Created"}})
			if !errors.Is(err, want) || calls != 1 {
				t.Fatal(result, err, want, calls)
			}
			if err != nil {
				if result.Page != (Info{}) || len(result.SiteIDs) != 0 {
					t.Fatal("partial result", result)
				}
			} else if result.Page.ID == "" || result.Page.URL != "/created/" {
				t.Fatal(result)
			}
			if mode == "cancel-before" && b.tx.writes != 0 {
				t.Fatal("write before canceled grant", b.tx.writes)
			}
		})
	}
}

func TestFlatPageSaveDomainAuthorizesEveryAffectedSiteBeforeWriting(t *testing.T) {
	b := flatFixture()
	b.links = append(b.links, linkInfo{ID: "00000000-0000-4000-8000-000000000101", PageID: flatID, SiteID: flatSiteB, URL: "/page/"})
	var calls []string
	var beforePointers []*Info
	store, err := New(Config{Backend: b, AuthorizeChange: func(_ context.Context, change Change) error {
		calls = append(calls, string(change.Action)+":"+change.SiteID)
		if change.Before == nil || change.Before.Content != "private body" {
			t.Fatal("borrowed or absent snapshot", change)
		}
		beforePointers = append(beforePointers, change.Before)
		change.Before.Content = "changed by policy"
		if change.Action == AttachSite {
			return ErrForbidden
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := SaveInput{Page: Draft{ID: flatID, URL: "/renamed/", Title: "Renamed"}, SiteIDs: []string{flatSiteC, flatSiteA, flatSiteA}}
	if saved, err := store.SaveDomain(context.Background(), input); !errors.Is(err, ErrForbidden) || saved.Page != (Info{}) {
		t.Fatal(saved, err)
	}
	want := []string{"update_page:", "retain_site:" + flatSiteA, "detach_site:" + flatSiteB, "attach_site:" + flatSiteC}
	if !reflect.DeepEqual(calls, want) || b.tx.writes != 0 || b.tx.rollbacks != 1 {
		t.Fatal(calls, b.tx.writes, b.tx.rollbacks)
	}
	for i := range beforePointers {
		for j := 0; j < i; j++ {
			if beforePointers[i] == beforePointers[j] {
				t.Fatal("reused mutable snapshot")
			}
		}
	}
	if input.SiteIDs[0] != flatSiteC {
		t.Fatal("caller IDs sorted in place")
	}
}

type flatMutationContext struct {
	context.Context
	mutate func()
}

func (c *flatMutationContext) Err() error {
	if c.mutate != nil {
		f := c.mutate
		c.mutate = nil
		f()
	}
	return c.Context.Err()
}

func TestFlatPageSaveDomainFreezesInputsAndRejectsPolicyDrift(t *testing.T) {
	for _, mode := range []string{"page", "mapping", "site", "config-and-input"} {
		t.Run(mode, func(t *testing.T) {
			b := flatFixture()
			ids := []string{flatSiteA}
			var store *Store
			var err error
			calls := 0
			store, err = New(Config{Backend: b, AuthorizeChange: func(_ context.Context, change Change) error {
				calls++
				if change.Action != RetainSite {
					return nil
				}
				switch mode {
				case "page":
					page := b.tx.pages[flatID]
					page.Title = "policy write"
					b.tx.pages[flatID] = page
				case "mapping":
					b.tx.links[0].URL = "/policy/"
				case "site":
					site := b.tx.sites[flatSiteA]
					site.Active = false
					b.tx.sites[flatSiteA] = site
				case "config-and-input":
					return ErrForbidden
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			ctx := &flatMutationContext{Context: context.Background()}
			if mode == "config-and-input" {
				ctx.mutate = func() { ids[0] = flatSiteB; *store = Store{} }
			}
			want := ErrUnavailable
			if mode == "config-and-input" {
				want = ErrForbidden
			}
			result, err := store.SaveDomain(ctx, SaveInput{Page: infoDraft(b.pages[flatID]), SiteIDs: ids})
			if !errors.Is(err, want) || result.Page != (Info{}) || calls != 2 || b.tx.writes != 0 {
				t.Fatal(result, err, calls, b.tx.writes)
			}
		})
	}
}

func TestFlatPageSaveDomainRejectsUnsafeConfigurationAndAmbient(t *testing.T) {
	b := flatFixture()
	reader, err := New(Config{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.SaveDomain(context.Background(), SaveInput{}); err != ErrConfiguration || b.begins != 0 {
		t.Fatal(err, b.begins)
	}
	store, _ := New(Config{Backend: b, MaxSites: 1, AuthorizeChange: func(context.Context, Change) error { return nil }})
	if _, err := store.SaveDomain(context.Background(), SaveInput{SiteIDs: []string{flatSiteA, flatSiteB}}); err != ErrLimit || b.begins != 0 {
		t.Fatal(err, b.begins)
	}
	if err := db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		if result, err := store.SaveDomain(ctx, SaveInput{Page: Draft{URL: "/new", Title: "New"}}); err != ErrTransaction || result.Page != (Info{}) {
			t.Fatal(result, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if b.begins != 1 {
		t.Fatal("nested owned transaction", b.begins)
	}
}

func TestFlatPageSaveDomainCallerKnownCreateIdentity(t *testing.T) {
	b := flatFixture()
	store, _ := New(Config{Backend: b, AuthorizeChange: func(context.Context, Change) error { return nil }})
	input := SaveInput{Create: true, Page: Draft{ID: flatID, URL: "/create/", Title: "Create"}}
	if result, err := store.SaveDomain(context.Background(), input); err != ErrConflict || result.Page != (Info{}) || b.tx.writes != 0 {
		t.Fatal(result, err, b.tx.writes)
	}
	input.Page.ID = "00000000-0000-4000-8000-000000000020"
	b.commitHook = func() error { return &db.Error{Code: db.UnknownCommit} }
	if result, err := store.SaveDomain(context.Background(), input); err != ErrOutcomeUnknown || result.Page != (Info{}) || input.Page.ID != "00000000-0000-4000-8000-000000000020" {
		t.Fatal(result, err, input)
	}
	if page, ok := b.tx.pages[input.Page.ID]; !ok || page.ID != input.Page.ID {
		t.Fatal("caller identity not used", page, ok)
	}
}
