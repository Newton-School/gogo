package integration_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/contrib/syndication"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/urls"
)

type feedArticle struct {
	models.Base
	ID                      int64
	Title, Body, Enclosure  string
	Public, EnclosurePublic bool
	Published, Updated      time.Time
}

func (*feedArticle) Schema() models.Schema {
	return models.Schema{AppLabel: "feeds", Name: "Article", Fields: []models.Field{
		models.BigIntegerField("id", models.Primary, models.WithStructField("ID")),
		models.CharField("title", models.WithStructField("Title"), models.WithMaxLength(200)),
		models.TextField("body", models.WithStructField("Body")),
		models.BooleanField("public", models.WithStructField("Public")),
		models.DateTimeField("published", models.WithStructField("Published")),
		models.DateTimeField("updated", models.WithStructField("Updated")),
		models.CharField("enclosure", models.WithStructField("Enclosure"), models.WithMaxLength(200), models.Optional),
		models.BooleanField("enclosure_public", models.WithStructField("EnclosurePublic")),
	}}
}

type syndicationFixture struct {
	t           *testing.T
	backend     db.Backend
	editor      db.SchemaEditor
	store       *orm.Store
	now         time.Time
	feedAllowed bool
	deniedItem  int64

	feedChecks, sourceCalls, itemChecks, enclosureChecks, sourceLimit, sourceRows int
}

func newSyndicationFixture(t *testing.T) *syndicationFixture {
	t.Helper()
	backend := testservice.Postgres(t)
	schema := (&feedArticle{}).Schema()
	if err := backend.SchemaEditor().CreateModel(context.Background(), backend, schema); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	f := &syndicationFixture{t: t, backend: backend, editor: backend.SchemaEditor(), store: orm.New(backend, registry), now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC), feedAllowed: true}
	// Insert out of order. Equal publication instants must use the explicit ID
	// tiebreaker; private and future rows must never enter the source snapshot.
	for _, article := range []*feedArticle{
		{ID: 3, Title: "Third public", Public: true, Published: f.now.Add(-time.Hour)},
		{ID: 1, Title: "Old public", Public: true, Published: f.now.Add(-3 * time.Hour)},
		{ID: 4, Title: "Private draft never emitted", Published: f.now.Add(-time.Minute)},
		{ID: 2, Title: "Second public", Public: true, Published: f.now.Add(-time.Hour), Enclosure: "/media/second.ogg", EnclosurePublic: true},
		{ID: 5, Title: "Future publication never emitted", Public: true, Published: f.now.Add(time.Hour)},
	} {
		article.Body, article.Updated = "Plain <b>article</b> & text", f.now
		if article.Published.After(article.Updated) {
			article.Updated = article.Published
		}
		if err := f.store.Save(context.Background(), article, orm.SaveOptions{ForceInsert: true}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *syndicationFixture) articles() orm.Query[*feedArticle] {
	return orm.For(f.store, func() *feedArticle { return &feedArticle{} })
}

func (f *syndicationFixture) publicArticles() orm.Query[*feedArticle] {
	return f.articles().Filter(orm.Q("public", true), orm.Q("published__lte", f.now))
}

func (f *syndicationFixture) source(ctx context.Context, limit int) (syndication.Feed, error) {
	f.sourceCalls++
	f.sourceLimit = limit
	rows, err := f.publicArticles().OrderBy("-published", "id").Limit(limit).All(ctx)
	if err != nil {
		return syndication.Feed{}, err
	}
	f.sourceRows = len(rows)
	feed := syndication.Feed{ID: "urn:feed:public-news", Title: "Public news", Link: "/news/", FeedURL: "/news/feed.xml", Description: syndication.Content{Value: "Public published articles"}, Author: &syndication.Author{Name: "News editor"}, Updated: f.now}
	for _, row := range rows {
		item := syndication.Item{ID: "urn:article:" + strconv.FormatInt(row.ID, 10), Title: row.Title, Link: "/news/articles/" + strconv.FormatInt(row.ID, 10), Description: syndication.Content{Value: row.Body}, Published: row.Published, Updated: row.Updated}
		if row.Enclosure != "" {
			item.Enclosures = []syndication.Enclosure{{URL: row.Enclosure, Length: 42, MIMEType: "audio/ogg"}}
		}
		feed.Items = append(feed.Items, item)
	}
	return feed, nil
}

func (f *syndicationFixture) authorize(context.Context) error {
	f.feedChecks++
	if !f.feedAllowed {
		return syndication.ErrNotPublic
	}
	return nil
}

func (f *syndicationFixture) authorizedArticle(ctx context.Context, item syndication.Item) (*feedArticle, error) {
	if !strings.HasPrefix(item.ID, "urn:article:") {
		return nil, syndication.ErrNotPublic
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(item.ID, "urn:article:"), 10, 64)
	if err != nil || id == f.deniedItem {
		return nil, syndication.ErrNotPublic
	}
	row, err := f.publicArticles().Filter(orm.Q("id", id)).Get(ctx)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, syndication.ErrNotPublic
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (f *syndicationFixture) itemPolicy(ctx context.Context, item syndication.Item) error {
	f.itemChecks++
	_, err := f.authorizedArticle(ctx, item)
	return err
}

func (f *syndicationFixture) enclosurePolicy(ctx context.Context, item syndication.Item, enclosure syndication.Enclosure) error {
	f.enclosureChecks++
	row, err := f.authorizedArticle(ctx, item)
	if err != nil {
		return err
	}
	if !row.EnclosurePublic || enclosure.URL != "https://example.test"+row.Enclosure {
		return syndication.ErrNotPublic
	}
	return nil
}

func (f *syndicationFixture) renderer(format syndication.Format, limit int) *syndication.Renderer {
	f.t.Helper()
	r, err := syndication.New(syndication.Config{Origin: "https://example.test", Format: format, MaxItems: limit, Policy: syndication.Policy{Item: f.itemPolicy, Enclosure: f.enclosurePolicy}})
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *syndicationFixture) handler(format syndication.Format, limit int, source syndication.Source) http.Handler {
	f.t.Helper()
	if source == nil {
		source = f.source
	}
	h, err := f.renderer(format, limit).Handler(source, syndication.HandlerOptions{Authorize: f.authorize, PublicCache: true})
	if err != nil {
		f.t.Fatal(err)
	}
	router, err := urls.New(urls.Include("news/", "news", urls.Path("feed.xml", h, "feed", http.MethodGet, http.MethodHead)))
	if err != nil {
		f.t.Fatal(err)
	}
	if path, err := router.Reverse("news:feed", nil, nil); err != nil || path != "/news/feed.xml" {
		f.t.Fatal(path, err)
	}
	return router
}

func syndicationRequest(h http.Handler, method, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://example.test/news/feed.xml", nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func syndicationTitles(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("response %d: %s", w.Code, w.Body.String())
	}
	var document struct {
		Channel struct {
			Items []struct {
				Title string `xml:"title"`
			} `xml:"item"`
		} `xml:"channel"`
		Entries []struct {
			Title string `xml:"title"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, item := range document.Channel.Items {
		titles = append(titles, item.Title)
	}
	for _, entry := range document.Entries {
		titles = append(titles, entry.Title)
	}
	return titles
}

func (f *syndicationFixture) update(id int64, values map[string]any) {
	f.t.Helper()
	n, err := f.articles().Filter(orm.Q("id", id)).Update(context.Background(), values)
	if err != nil || n != 1 {
		f.t.Fatal("fixture update", n, err)
	}
}

func requireNoSyndication(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "<rss") || strings.Contains(w.Body.String(), "<feed") || strings.Contains(w.Body.String(), "Public news") || strings.Contains(w.Body.String(), "Private draft") || strings.Contains(w.Body.String(), "feeds_article") {
		t.Fatalf("failure published feed or details: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestPostgresSyndicationPublicSourceOrderAndChangedRepresentation(t *testing.T) {
	for _, format := range []syndication.Format{syndication.RSS2{}, syndication.Atom1{}} {
		t.Run(format.ContentType(), func(t *testing.T) {
			f := newSyndicationFixture(t)
			h := f.handler(format, 3, nil)
			first := syndicationRequest(h, "GET", "")
			if got := syndicationTitles(t, first); !reflect.DeepEqual(got, []string{"Second public", "Third public", "Old public"}) {
				t.Fatal(got)
			}
			if f.sourceLimit != 4 || f.sourceRows != 3 || f.feedChecks != 1 || f.itemChecks != 3 || f.enclosureChecks != 1 {
				t.Fatalf("boundary counts: limit=%d rows=%d feed=%d items=%d enclosures=%d", f.sourceLimit, f.sourceRows, f.feedChecks, f.itemChecks, f.enclosureChecks)
			}
			if strings.Contains(first.Body.String(), "never emitted") || !strings.Contains(first.Body.String(), "https://example.test/media/second.ogg") || first.Header().Get("ETag") == "" {
				t.Fatal("visibility or absolute enclosure mismatch")
			}
			initialTag := first.Header().Get("ETag")
			unchanged := syndicationRequest(h, "GET", "W/"+initialTag)
			if unchanged.Code != 304 || unchanged.Body.Len() != 0 || f.sourceCalls != 2 || f.itemChecks != 6 || f.enclosureChecks != 2 {
				t.Fatal("validator bypassed current source or policies", unchanged.Code)
			}
			// Neither change advances item/feed timestamps. A last-modified-only
			// shortcut would incorrectly reuse a document containing withdrawn data.
			f.update(3, map[string]any{"public": false})
			hidden := syndicationRequest(h, "GET", initialTag)
			if got := syndicationTitles(t, hidden); !reflect.DeepEqual(got, []string{"Second public", "Old public"}) || hidden.Header().Get("ETag") == initialTag {
				t.Fatal("visibility change produced stale response", got, hidden.Header())
			}
			row, err := f.articles().Filter(orm.Q("id", int64(2))).Get(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			counts, err := f.store.Delete(context.Background(), row)
			if err != nil || counts[(&feedArticle{}).Schema().Key()] != 1 {
				t.Fatal("fixture delete", counts, err)
			}
			removed := syndicationRequest(h, "GET", hidden.Header().Get("ETag"))
			if got := syndicationTitles(t, removed); !reflect.DeepEqual(got, []string{"Old public"}) || removed.Header().Get("ETag") == hidden.Header().Get("ETag") || strings.Contains(removed.Body.String(), "/media/second.ogg") {
				t.Fatal("removal produced stale feed", got, removed.Header())
			}
			if modified := removed.Header().Get("Last-Modified"); modified != "" {
				t.Fatal("item timestamps used as representation validator", modified)
			}
			head := syndicationRequest(h, "HEAD", removed.Header().Get("ETag"))
			if head.Code != 304 || head.Body.Len() != 0 || f.sourceCalls != 5 {
				t.Fatal("HEAD conditional contract", head.Code, f.sourceCalls)
			}
		})
	}
}

func TestPostgresSyndicationOverflowRejectsInsteadOfTruncating(t *testing.T) {
	f := newSyndicationFixture(t)
	h := f.handler(syndication.RSS2{}, 2, nil)
	w := syndicationRequest(h, "GET", "")
	requireNoSyndication(t, w, 503)
	if f.sourceLimit != 3 || f.sourceRows != 3 || f.itemChecks != 0 || f.enclosureChecks != 0 {
		t.Fatal("overflow sentinel was silently sliced", f.sourceLimit, f.sourceRows, f.itemChecks)
	}
	f.update(1, map[string]any{"public": false})
	w = syndicationRequest(h, "GET", "")
	if got := syndicationTitles(t, w); !reflect.DeepEqual(got, []string{"Second public", "Third public"}) || f.sourceLimit != 3 || f.sourceRows != 2 {
		t.Fatal("exact cap not accepted", got)
	}
}

func TestPostgresSyndicationFeedItemEnclosureAndSourceFailures(t *testing.T) {
	for _, mode := range []string{"feed", "item", "enclosure", "withdrawn_after_source", "source_database"} {
		t.Run(mode, func(t *testing.T) {
			f := newSyndicationFixture(t)
			h := f.handler(syndication.RSS2{}, 3, nil)
			initial := syndicationRequest(h, "GET", "")
			if initial.Code != 200 {
				t.Fatal(initial.Code, initial.Body.String())
			}
			want := 403
			beforeSources := f.sourceCalls
			switch mode {
			case "feed":
				f.feedAllowed = false
			case "item":
				f.deniedItem = 3
			case "enclosure":
				f.update(2, map[string]any{"enclosure_public": false})
			case "withdrawn_after_source":
				h = f.handler(syndication.RSS2{}, 3, func(ctx context.Context, limit int) (syndication.Feed, error) {
					feed, err := f.source(ctx, limit)
					if err != nil {
						return feed, err
					}
					// Simulate a committed publication change between source reading
					// and the application's fresh item authorization lookup.
					_, err = f.articles().Filter(orm.Q("id", int64(2))).Update(ctx, map[string]any{"public": false})
					return feed, err
				})
			case "source_database":
				want = 503
				if err := f.editor.DeleteModel(context.Background(), f.backend, (&feedArticle{}).Schema()); err != nil {
					t.Fatal(err)
				}
			}
			w := syndicationRequest(h, "GET", initial.Header().Get("ETag"))
			requireNoSyndication(t, w, want)
			if mode == "feed" && f.sourceCalls != beforeSources {
				t.Fatal("feed denial invoked ORM source")
			}
			if mode != "feed" && f.sourceCalls != beforeSources+1 {
				t.Fatal("conditional request skipped source")
			}
		})
	}
}

func TestPostgresSyndicationRejectsInvalidFormatMetadata(t *testing.T) {
	for _, mode := range []string{"category_scheme", "enclosure_media_type"} {
		t.Run(mode, func(t *testing.T) {
			f := newSyndicationFixture(t)
			h := f.handler(syndication.Atom1{}, 3, func(ctx context.Context, limit int) (syndication.Feed, error) {
				feed, err := f.source(ctx, limit)
				if err != nil {
					return feed, err
				}
				if mode == "category_scheme" {
					feed.Items[0].Categories = []syndication.Category{{Term: "news", Scheme: "not an IRI"}}
				} else {
					feed.Items[0].Enclosures[0].MIMEType = "audio"
				}
				return feed, nil
			})
			requireNoSyndication(t, syndicationRequest(h, "GET", ""), 503)
		})
	}
}
