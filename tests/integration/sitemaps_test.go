package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/contrib/sitemaps"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/urls"
)

// Reuse the public/future/private article fixture, not the feed runtime. This
// provider derives each page and its revision from one bounded ORM snapshot.
type sitemapDBFixture struct {
	articles *syndicationFixture
	pageSize int
	query    string

	sectionAllowed   bool
	indexAllowed     bool
	alternateAllowed bool
	deniedID         int64

	manifestCalls   int
	loadCalls       int
	sectionChecks   int
	entryChecks     int
	indexChecks     int
	alternateChecks int
	manifestLimit   int
	loadLimits      sitemaps.Limits

	afterManifest            func() error
	afterLoad                func() error
	manifestError, loadError error
}

func newSitemapDBFixture(t *testing.T) *sitemapDBFixture {
	return &sitemapDBFixture{articles: newSyndicationFixture(t), pageSize: 2, sectionAllowed: true, indexAllowed: true, alternateAllowed: true}
}

func (f *sitemapDBFixture) snapshot(ctx context.Context, pageLimit int) ([]sitemaps.Page, error) {
	if pageLimit < 1 || pageLimit > 1001 || f.pageSize < 1 || f.pageSize > 10 {
		return nil, sitemaps.ErrLimit
	}
	limit := pageLimit*f.pageSize + 1
	rows, err := f.articles.publicArticles().OrderBy("-published", "id").Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == limit {
		return nil, sitemaps.ErrLimit
	}
	var pages []sitemaps.Page
	for start := 0; start < len(rows); start += f.pageSize {
		page := sitemaps.Page{}
		for _, row := range rows[start:min(start+f.pageSize, len(rows))] {
			loc := "/news/articles/" + strconv.FormatInt(row.ID, 10) + f.query
			entry := sitemaps.Entry{Loc: loc, LastMod: sitemaps.LastModified{Time: row.Updated}, ChangeFreq: "weekly"}
			if row.ID == 2 {
				entry.Alternates = []sitemaps.Alternate{{Language: "en", Loc: loc}, {Language: "fr", Loc: "https://fr.example.test/articles/2"}}
			}
			page.Entries = append(page.Entries, entry)
		}
		wire, err := json.Marshal(page.Entries)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(wire)
		page.Ref = sitemaps.PageRef{Key: "p" + strconv.Itoa(len(pages)+1), Revision: hex.EncodeToString(sum[:])}
		pages = append(pages, page)
	}
	return pages, nil
}

func (f *sitemapDBFixture) Manifest(ctx context.Context, limit int) ([]sitemaps.PageRef, error) {
	f.manifestCalls++
	f.manifestLimit = limit
	if f.manifestError != nil {
		return nil, f.manifestError
	}
	pages, err := f.snapshot(ctx, limit)
	if err != nil {
		return nil, err
	}
	refs := make([]sitemaps.PageRef, len(pages))
	for i, page := range pages {
		refs[i] = page.Ref
	}
	if f.afterManifest != nil {
		if err := f.afterManifest(); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func (f *sitemapDBFixture) LoadPage(ctx context.Context, ref sitemaps.PageRef, limits sitemaps.Limits) (sitemaps.Page, error) {
	f.loadCalls++
	f.loadLimits = limits
	if f.loadError != nil {
		return sitemaps.Page{}, f.loadError
	}
	// The fixture has at most five rows; the explicit bounded query is still
	// part of the provider contract, even for a reference already in a manifest.
	pages, err := f.snapshot(ctx, 32)
	if err != nil {
		return sitemaps.Page{}, err
	}
	for _, page := range pages {
		if page.Ref.Key != ref.Key {
			continue
		}
		if page.Ref.Revision != ref.Revision {
			return sitemaps.Page{}, sitemaps.ErrStale
		}
		if len(page.Entries) > limits.MaxItems {
			return sitemaps.Page{}, sitemaps.ErrLimit
		}
		if f.afterLoad != nil {
			if err := f.afterLoad(); err != nil {
				return sitemaps.Page{}, err
			}
		}
		return page, nil
	}
	return sitemaps.Page{}, sitemaps.ErrStale
}

func (f *sitemapDBFixture) authorizedArticle(ctx context.Context, entry sitemaps.Entry) error {
	u, err := url.Parse(entry.Loc)
	if err != nil || u.Scheme != "https" || u.Host != "example.test" || !strings.HasPrefix(u.Path, "/news/articles/") {
		return sitemaps.ErrNotPublic
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(u.Path, "/news/articles/"), 10, 64)
	if err != nil || id == f.deniedID {
		return sitemaps.ErrNotPublic
	}
	_, err = f.articles.publicArticles().Filter(orm.Q("id", id)).Get(ctx)
	if errors.Is(err, orm.ErrNotFound) {
		return sitemaps.ErrNotPublic
	}
	return err
}

func (f *sitemapDBFixture) handler(maxItems, maxBytes, maxPages int) http.Handler {
	f.articles.t.Helper()
	r, err := sitemaps.New(sitemaps.Config{Origin: "https://example.test", Directory: "/news/", AdditionalOrigins: []string{"https://fr.example.test"}, MaxItems: maxItems, MaxBytes: maxBytes, MaxPages: maxPages, Policy: sitemaps.Policy{
		Entry: func(ctx context.Context, entry sitemaps.Entry) error {
			f.entryChecks++
			return f.authorizedArticle(ctx, entry)
		},
		Alternate: func(ctx context.Context, entry sitemaps.Entry, alternate sitemaps.Alternate) error {
			f.alternateChecks++
			if err := f.authorizedArticle(ctx, entry); err != nil {
				return err
			}
			if !f.alternateAllowed {
				return sitemaps.ErrNotPublic
			}
			if alternate.Language == "en" && alternate.Loc == entry.Loc || alternate.Language == "fr" && alternate.Loc == "https://fr.example.test/articles/2" {
				return nil
			}
			return sitemaps.ErrNotPublic
		},
		Index: func(_ context.Context, entry sitemaps.IndexEntry) error {
			f.indexChecks++
			if !f.indexAllowed || !f.sectionAllowed || !strings.HasPrefix(entry.Loc, "https://example.test/news/sitemap-news.p") {
				return sitemaps.ErrNotPublic
			}
			return nil
		},
	}})
	if err != nil {
		f.articles.t.Fatal(err)
	}
	routes, err := r.Routes([]sitemaps.Section{{Name: "news", Provider: f}}, sitemaps.HandlerOptions{PublicCache: true, Authorize: func(_ context.Context, section string) error {
		f.sectionChecks++
		if section != "news" || !f.sectionAllowed {
			return sitemaps.ErrNotPublic
		}
		return nil
	}})
	if err != nil {
		f.articles.t.Fatal(err)
	}
	router, err := urls.New(urls.Include("", "public", routes...))
	if err != nil {
		f.articles.t.Fatal(err)
	}
	for name, params := range map[string]map[string]any{"public:sitemap_index": nil, "public:sitemap_page": {"section": "news", "page": "p1"}} {
		want := "/news/sitemap.xml"
		if params != nil {
			want = "/news/sitemap-news.p1.xml"
		}
		if got, err := router.Reverse(name, params, nil); err != nil || got != want {
			f.articles.t.Fatal(name, got, err)
		}
	}
	return router
}

func sitemapDBRequest(handler http.Handler, method, path, etag string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://untrusted-host.test"+path, nil)
	if etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sitemapDBLocations(t *testing.T, w *httptest.ResponseRecorder, index bool) []string {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("response %d: %s", w.Code, w.Body.String())
	}
	var parsed struct {
		XMLName xml.Name
		URLs    []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
		Maps []struct {
			Loc string `xml:"loc"`
		} `xml:"sitemap"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	wantRoot := "urlset"
	if index {
		wantRoot = "sitemapindex"
	}
	if parsed.XMLName.Local != wantRoot || parsed.XMLName.Space != "http://www.sitemaps.org/schemas/sitemap/0.9" {
		t.Fatal(parsed.XMLName)
	}
	var result []string
	for _, row := range parsed.URLs {
		result = append(result, row.Loc)
	}
	for _, row := range parsed.Maps {
		result = append(result, row.Loc)
	}
	return result
}

func requireNoSitemapDB(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "<urlset") || strings.Contains(w.Body.String(), "<sitemapindex") || strings.Contains(w.Body.String(), "feeds_article") || strings.Contains(w.Body.String(), "private detail") {
		t.Fatalf("failure published sitemap or provider detail: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestPostgresSitemapPublicOrderingRevisionsRoutesAndVisibility(t *testing.T) {
	f := newSitemapDBFixture(t)
	h := f.handler(2, 0, 10)
	index := sitemapDBRequest(h, "GET", "/news/sitemap.xml", "")
	if got := sitemapDBLocations(t, index, true); !reflect.DeepEqual(got, []string{"https://example.test/news/sitemap-news.p1.xml", "https://example.test/news/sitemap-news.p2.xml"}) {
		t.Fatal(got)
	}
	if f.manifestLimit != 3 || f.loadCalls != 0 || f.indexChecks != 2 {
		t.Fatal("manifest overflow grant boundary", f.manifestLimit, f.loadCalls, f.indexChecks)
	}
	first := sitemapDBRequest(h, "GET", "/news/sitemap-news.p1.xml", "")
	if got := sitemapDBLocations(t, first, false); !reflect.DeepEqual(got, []string{"https://example.test/news/articles/2", "https://example.test/news/articles/3"}) {
		t.Fatal(got)
	}
	second := sitemapDBRequest(h, "GET", "/news/sitemap-news.p2.xml", "")
	if got := sitemapDBLocations(t, second, false); !reflect.DeepEqual(got, []string{"https://example.test/news/articles/1"}) {
		t.Fatal(got)
	}
	if f.loadLimits.MaxItems != 2 || f.entryChecks != 3 || f.alternateChecks != 2 || !strings.Contains(first.Body.String(), "https://fr.example.test/articles/2") || strings.Contains(first.Body.String(), "untrusted-host") {
		t.Fatal("scoping, alternate, or load limits", f.loadLimits, f.entryChecks, f.alternateChecks, first.Body.String())
	}
	beforeLoads, beforeChecks := f.loadCalls, f.entryChecks
	unchanged := sitemapDBRequest(h, "GET", "/news/sitemap-news.p1.xml", "W/"+first.Header().Get("ETag"))
	if unchanged.Code != 304 || unchanged.Body.Len() != 0 || f.loadCalls != beforeLoads+1 || f.entryChecks != beforeChecks+2 {
		t.Fatal("conditional skipped current ORM/auth checks", unchanged.Code, f.loadCalls, f.entryChecks)
	}
	// Visibility changes intentionally leave timestamps untouched.
	f.articles.update(3, map[string]any{"public": false})
	changed := sitemapDBRequest(h, "GET", "/news/sitemap-news.p1.xml", first.Header().Get("ETag"))
	if got := sitemapDBLocations(t, changed, false); !reflect.DeepEqual(got, []string{"https://example.test/news/articles/2", "https://example.test/news/articles/1"}) || changed.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatal("stale public page", got, changed.Header())
	}
	newIndex := sitemapDBRequest(h, "GET", "/news/sitemap.xml", index.Header().Get("ETag"))
	if got := sitemapDBLocations(t, newIndex, true); len(got) != 1 || newIndex.Header().Get("ETag") == index.Header().Get("ETag") {
		t.Fatal("removed page stayed in index", got, newIndex.Header())
	}
	row, err := f.articles.articles().Filter(orm.Q("id", int64(2))).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts, err := f.articles.store.Delete(context.Background(), row); err != nil || counts[(&feedArticle{}).Schema().Key()] != 1 {
		t.Fatal(counts, err)
	}
	removed := sitemapDBRequest(h, "GET", "/news/sitemap-news.p1.xml", changed.Header().Get("ETag"))
	if got := sitemapDBLocations(t, removed, false); !reflect.DeepEqual(got, []string{"https://example.test/news/articles/1"}) || removed.Header().Get("ETag") == changed.Header().Get("ETag") || strings.Contains(removed.Body.String(), "fr.example.test") {
		t.Fatal("removed row or alternates survived", got, removed.Header())
	}
	if removed.Header().Get("Last-Modified") != "" {
		t.Fatal("item timestamps used as conditional shortcut")
	}
	head := sitemapDBRequest(h, "HEAD", "/news/sitemap-news.p1.xml", removed.Header().Get("ETag"))
	if head.Code != 304 || head.Body.Len() != 0 {
		t.Fatal(head.Code, head.Body.String())
	}
}

func TestPostgresSitemapStaleBetweenManifestAndLoadIsAtomic(t *testing.T) {
	f := newSitemapDBFixture(t)
	h := f.handler(2, 0, 10)
	initial := sitemapDBRequest(h, "GET", "/news/sitemap-news.p1.xml", "")
	sitemapDBLocations(t, initial, false)
	f.afterManifest = func() error {
		_, err := f.articles.articles().Filter(orm.Q("id", int64(3))).Update(context.Background(), map[string]any{"public": false})
		return err
	}
	requireNoSitemapDB(t, sitemapDBRequest(h, "GET", "/news/sitemap-news.p1.xml", initial.Header().Get("ETag")), 503)
	if f.entryChecks != 2 {
		t.Fatal("stale reference invoked item grants", f.entryChecks)
	}
	f.afterManifest = nil
	if got := sitemapDBLocations(t, sitemapDBRequest(h, "GET", "/news/sitemap-news.p1.xml", initial.Header().Get("ETag")), false); len(got) != 2 || strings.HasSuffix(got[1], "/3") {
		t.Fatal("fresh revision did not recover", got)
	}
}

func TestPostgresSitemapAuthorityAndProviderFailuresDoNotPublish(t *testing.T) {
	for _, mode := range []string{"section", "index", "item", "alternate", "withdrawn_after_load", "database", "manifest_error", "load_error", "missing_section", "missing_page"} {
		t.Run(mode, func(t *testing.T) {
			f := newSitemapDBFixture(t)
			h := f.handler(2, 0, 10)
			path := "/news/sitemap-news.p1.xml"
			initial := sitemapDBRequest(h, "GET", path, "")
			sitemapDBLocations(t, initial, false)
			beforeManifest, beforeLoad := f.manifestCalls, f.loadCalls
			want := 403
			switch mode {
			case "section":
				f.sectionAllowed = false
			case "index":
				f.indexAllowed = false
				path = "/news/sitemap.xml"
			case "item":
				f.deniedID = 3
			case "alternate":
				f.alternateAllowed = false
			case "withdrawn_after_load":
				f.afterLoad = func() error {
					_, err := f.articles.articles().Filter(orm.Q("id", int64(2))).Update(context.Background(), map[string]any{"public": false})
					return err
				}
			case "database":
				want = 503
				if err := f.articles.editor.DeleteModel(context.Background(), f.articles.backend, (&feedArticle{}).Schema()); err != nil {
					t.Fatal(err)
				}
			case "manifest_error":
				want = 503
				f.manifestError = errors.New("private detail")
			case "load_error":
				want = 503
				f.loadError = errors.New("private detail")
			case "missing_section":
				want = 404
				path = "/news/sitemap-other.p1.xml"
			case "missing_page":
				want = 404
				path = "/news/sitemap-news.absent.xml"
			}
			requireNoSitemapDB(t, sitemapDBRequest(h, "GET", path, initial.Header().Get("ETag")), want)
			if (mode == "section" || mode == "missing_section") && (f.manifestCalls != beforeManifest || f.loadCalls != beforeLoad) {
				t.Fatal("denied/unknown section read provider")
			}
			if mode == "missing_page" && (f.manifestCalls != beforeManifest+1 || f.loadCalls != beforeLoad) {
				t.Fatal("unknown key loaded data")
			}
		})
	}
}

func TestPostgresSitemapCountAndEscapedByteOverflowAreNotTruncated(t *testing.T) {
	for _, mode := range []string{"manifest_count", "page_count", "xml_bytes"} {
		t.Run(mode, func(t *testing.T) {
			f := newSitemapDBFixture(t)
			items, bytes, pages := 2, 0, 10
			path := "/news/sitemap-news.p1.xml"
			switch mode {
			case "manifest_count":
				pages = 1
				path = "/news/sitemap.xml"
			case "page_count":
				f.pageSize = 3
			case "xml_bytes":
				bytes = 1024
				f.query = "?" + strings.Repeat("x=1&", 200) + "last=1"
			}
			h := f.handler(items, bytes, pages)
			requireNoSitemapDB(t, sitemapDBRequest(h, "GET", path, ""), 503)
			if mode == "manifest_count" && (f.indexChecks != 0 || f.loadCalls != 0 || f.manifestLimit != 2) {
				t.Fatal("manifest sentinel was truncated", f.indexChecks, f.loadCalls, f.manifestLimit)
			}
			if mode == "page_count" && (f.entryChecks != 0 || f.loadLimits.MaxItems != 2) {
				t.Fatal("count overflow invoked item policy", f.entryChecks, f.loadLimits)
			}
			// Remove the overflow condition, without rebuilding the router's frozen configuration.
			switch mode {
			case "manifest_count":
				f.articles.update(1, map[string]any{"public": false})
			case "page_count":
				f.pageSize = 2
			case "xml_bytes":
				f.query = ""
			}
			if w := sitemapDBRequest(h, "GET", path, ""); w.Code != 200 {
				t.Fatal("bounded retry did not recover", w.Code, w.Body.String())
			}
		})
	}
}
