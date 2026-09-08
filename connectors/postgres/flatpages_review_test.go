package postgres_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/Newton-School/gogo/core/contrib/flatpages"
	"github.com/Newton-School/gogo/core/db"
)

func TestPostgresFlatpagesReviewAuthorizesCompleteSortedSiteReplacement(t *testing.T) {
	b, _, sites, _ := setupFlatpages(t)
	ctx := context.Background()
	var changes []flatpages.Change
	store := newFlatStore(t, b, func(ctx context.Context, change flatpages.Change) error {
		changes = append(changes, change)
		if !db.InTransaction(ctx, b.Alias()) {
			t.Error("authorization did not run inside the owned transaction")
		}
		return nil
	})
	saved, err := store.SaveDomain(ctx, flatpages.SaveInput{
		Page:    flatpages.Draft{URL: "/shared", Title: "Original", Content: "Original content"},
		SiteIDs: []string{sites[1].ID, sites[0].ID, sites[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstSites := []string{sites[0].ID, sites[1].ID}
	slices.Sort(firstSites)
	if !slices.Equal(saved.SiteIDs, firstSites) || len(changes) != 3 || changes[0].Action != flatpages.CreatePage || changes[0].SiteID != "" {
		t.Fatal("create authorization", saved.SiteIDs, changes)
	}
	for i, change := range changes {
		if change.Before != nil || change.After != saved.Page || i > 0 && (change.Action != flatpages.AttachSite || change.SiteID != firstSites[i-1]) {
			t.Fatal("create grant snapshot", change)
		}
	}

	before := saved.Page
	draft := flatpages.Draft(before)
	draft.URL, draft.Title = "/changed", "Changed"
	changes = nil
	store = newFlatStore(t, b, func(ctx context.Context, change flatpages.Change) error {
		// Every callback sees the same original state, even if an earlier
		// callback changed its detached Before value.
		if change.Before == nil || *change.Before != before || change.After != flatpages.Info(draft) {
			t.Error("authorization snapshot changed", change)
		}
		var title string
		if err := db.QueryRow(ctx, db.ExecutorFor(ctx, b), `SELECT "title" FROM "gogo_flatpages" WHERE "id"=$1`, []any{before.ID}, &title); err != nil || title != before.Title {
			t.Error("writes began before all grants", title, err)
		}
		changes = append(changes, change)
		if change.Before != nil {
			change.Before.Title = "callback-local mutation"
		}
		return nil
	})
	saved, err = store.SaveDomain(ctx, flatpages.SaveInput{Page: draft, SiteIDs: []string{sites[2].ID, sites[1].ID}})
	if err != nil {
		t.Fatal(err)
	}
	union := []string{sites[0].ID, sites[1].ID, sites[2].ID}
	slices.Sort(union)
	want := map[string]flatpages.ChangeAction{sites[0].ID: flatpages.DetachSite, sites[1].ID: flatpages.RetainSite, sites[2].ID: flatpages.AttachSite}
	if len(changes) != 4 || changes[0].Action != flatpages.UpdatePage || changes[0].SiteID != "" {
		t.Fatal("update authorization", changes)
	}
	for i, id := range union {
		if changes[i+1].SiteID != id || changes[i+1].Action != want[id] {
			t.Fatal("missing or unordered association grant", changes)
		}
	}
	assertFlatpageReviewState(t, b, saved.Page, []string{sites[1].ID, sites[2].ID})
}

func TestPostgresFlatpagesReviewDeniedActionCannotPartiallySave(t *testing.T) {
	for _, denied := range []flatpages.ChangeAction{flatpages.CreatePage, flatpages.UpdatePage, flatpages.AttachSite, flatpages.RetainSite, flatpages.DetachSite} {
		t.Run(string(denied), func(t *testing.T) {
			b, _, sites, _ := setupFlatpages(t)
			ctx := context.Background()
			allowed := newFlatStore(t, b, func(context.Context, flatpages.Change) error { return nil })
			original, err := allowed.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/original", Title: "Original"}, SiteIDs: []string{sites[0].ID, sites[1].ID}})
			if err != nil {
				t.Fatal(err)
			}
			input := flatpages.SaveInput{Page: flatpages.Draft(original.Page), SiteIDs: []string{sites[1].ID, sites[2].ID}}
			input.Page.URL, input.Page.Title = "/attempted", "Attempted"
			if denied == flatpages.CreatePage {
				input.Page.ID = ""
			}
			seen := false
			store := newFlatStore(t, b, func(_ context.Context, change flatpages.Change) error {
				if change.Action == denied {
					seen = true
					return flatpages.ErrForbidden
				}
				return nil
			})
			saved, err := store.SaveDomain(ctx, input)
			if err != flatpages.ErrForbidden || !reflect.DeepEqual(saved, flatpages.Saved{}) || !seen {
				t.Fatal("denial result", saved, err, seen)
			}
			assertFlatpageReviewState(t, b, original.Page, []string{sites[0].ID, sites[1].ID})
			var count int
			if err := db.QueryRow(ctx, b, `SELECT count(*) FROM "gogo_flatpages"`, nil, &count); err != nil || count != 1 {
				t.Fatal("denied create left a page", count, err)
			}
		})
	}
}

func TestPostgresFlatpagesReviewLastGrantCannotRetargetEarlierRows(t *testing.T) {
	for _, target := range []string{"page", "earlier mapping", "earlier site"} {
		t.Run(target, func(t *testing.T) {
			b, _, sites, _ := setupFlatpages(t)
			ctx := context.Background()
			ids := []string{sites[0].ID, sites[1].ID, sites[2].ID}
			slices.Sort(ids)
			store := newFlatStore(t, b, func(context.Context, flatpages.Change) error { return nil })
			original, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/original", Title: "Original"}, SiteIDs: ids})
			if err != nil {
				t.Fatal(err)
			}
			mutated := false
			store = newFlatStore(t, b, func(txctx context.Context, change flatpages.Change) error {
				if change.SiteID != ids[len(ids)-1] {
					return nil
				}
				mutated = true
				executor := db.ExecutorFor(txctx, b)
				var err error
				switch target {
				case "page":
					_, err = executor.Exec(txctx, `UPDATE "gogo_flatpages" SET "title"='policy drift' WHERE "id"=$1`, original.Page.ID)
				case "earlier mapping":
					_, err = executor.Exec(txctx, `UPDATE "gogo_flatpage_sites" SET "url"='/policy-drift' WHERE "page_id"=$1 AND "site_id"=$2`, original.Page.ID, ids[0])
				case "earlier site":
					_, err = executor.Exec(txctx, `UPDATE "gogo_sites" SET "active"=false WHERE "id"=$1`, ids[0])
				}
				return err
			})
			draft := flatpages.Draft(original.Page)
			draft.URL, draft.Title = "/changed", "Changed"
			saved, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: draft, SiteIDs: ids})
			if !mutated || err != flatpages.ErrUnavailable || !reflect.DeepEqual(saved, flatpages.Saved{}) {
				t.Fatal("late grant mutation escaped final fence", mutated, saved, err)
			}
			assertFlatpageReviewState(t, b, original.Page, ids)
			var active bool
			if err := db.QueryRow(ctx, b, `SELECT "active" FROM "gogo_sites" WHERE "id"=$1`, []any{ids[0]}, &active); err != nil || !active {
				t.Fatal("policy side effect was not rolled back", active, err)
			}
		})
	}
}

func TestPostgresFlatpagesReviewPostwriteDriftRollsBack(t *testing.T) {
	for _, target := range []string{"page", "mapping", "site"} {
		t.Run(target, func(t *testing.T) {
			b, _, sites, _ := setupFlatpages(t)
			ctx := context.Background()
			ids := []string{sites[0].ID, sites[1].ID}
			store := newFlatStore(t, b, func(context.Context, flatpages.Change) error { return nil })
			original, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/original", Title: "Original"}, SiteIDs: ids})
			if err != nil {
				t.Fatal(err)
			}
			body := `NEW.title := 'trigger drift';`
			timing := "BEFORE"
			if target == "mapping" {
				body = `UPDATE "gogo_flatpage_sites" SET "url"='/trigger-drift' WHERE "page_id"=NEW.id;`
				timing = "AFTER"
			} else if target == "site" {
				body = `UPDATE "gogo_sites" SET "active"=false WHERE "id" IN (SELECT "site_id" FROM "gogo_flatpage_sites" WHERE "page_id"=NEW.id);`
				timing = "AFTER"
			}
			if _, err := b.Exec(ctx, `CREATE FUNCTION flatpage_review_drift() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` RETURN NEW; END $$`); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Exec(ctx, `CREATE TRIGGER flatpage_review_drift `+timing+` UPDATE ON "gogo_flatpages" FOR EACH ROW EXECUTE FUNCTION flatpage_review_drift()`); err != nil {
				t.Fatal(err)
			}
			draft := flatpages.Draft(original.Page)
			draft.Title = "Attempted"
			saved, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: draft, SiteIDs: ids})
			if err != flatpages.ErrUnavailable || !reflect.DeepEqual(saved, flatpages.Saved{}) {
				t.Fatal("postwrite mutation escaped final fence", saved, err)
			}
			assertFlatpageReviewState(t, b, original.Page, ids)
			var active int
			if err := db.QueryRow(ctx, b, `SELECT count(*) FROM "gogo_sites" WHERE "active"=true`, nil, &active); err != nil || active != len(sites) {
				t.Fatal("trigger side effects were not rolled back", active, err)
			}
		})
	}
}

func assertFlatpageReviewState(t *testing.T, executor db.Executor, want flatpages.Info, siteIDs []string) {
	t.Helper()
	ctx := context.Background()
	var got flatpages.Info
	err := db.QueryRow(ctx, executor, `SELECT "id", "url", "title", "content", "template_name", "registration_required" FROM "gogo_flatpages" WHERE "id"=$1`, []any{want.ID}, &got.ID, &got.URL, &got.Title, &got.Content, &got.TemplateName, &got.RegistrationRequired)
	if err != nil || got != want {
		t.Fatal("page state", got, want, err)
	}
	rows, err := executor.Query(ctx, `SELECT "site_id", "url" FROM "gogo_flatpage_sites" WHERE "page_id"=$1 ORDER BY "site_id"`, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotIDs []string
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil || path != want.URL {
			t.Fatal("mapping state", id, path, err)
		}
		gotIDs = append(gotIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantIDs := slices.Clone(siteIDs)
	slices.Sort(wantIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatal("association state", gotIDs, wantIDs)
	}
}
