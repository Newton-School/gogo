package sitemaps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testProvider struct {
	manifest func(context.Context, int) ([]PageRef, error)
	load     func(context.Context, PageRef, Limits) (Page, error)
}

func (p *testProvider) Manifest(ctx context.Context, limit int) ([]PageRef, error) {
	return p.manifest(ctx, limit)
}
func (p *testProvider) LoadPage(ctx context.Context, ref PageRef, limits Limits) (Page, error) {
	return p.load(ctx, ref, limits)
}

func handlerConfig() Config {
	return Config{Origin: "https://example.test", MaxItems: 10, MaxPages: 10, Policy: Policy{
		Entry: func(context.Context, Entry) error { return nil }, Index: func(context.Context, IndexEntry) error { return nil },
	}}
}

func allowSection(context.Context, string) error { return nil }

func handlerProvider() *testProvider {
	return &testProvider{
		manifest: func(context.Context, int) ([]PageRef, error) { return []PageRef{{Key: "one", Revision: "v1"}}, nil },
		load: func(_ context.Context, ref PageRef, _ Limits) (Page, error) {
			return Page{Ref: ref, Entries: []Entry{{Loc: "/articles/one"}}}, nil
		},
	}
}

func newHandlerTest(t *testing.T, provider Provider, options HandlerOptions) http.Handler {
	t.Helper()
	r, err := New(handlerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if options.Authorize == nil {
		options.Authorize = allowSection
	}
	h, err := r.Handler([]Section{{Name: "news", Provider: provider}}, options)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func sitemapRequest(h http.Handler, method, path, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://example.test"+path, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestSitemapHTTPIndexPageAndConditionalReauthorization(t *testing.T) {
	p := handlerProvider()
	manifest, load := p.manifest, p.load
	manifests, loads, grants := 0, 0, 0
	p.manifest = func(ctx context.Context, limit int) ([]PageRef, error) {
		manifests++
		if limit != 11 {
			t.Fatal("missing overflow sentinel", limit)
		}
		return manifest(ctx, limit)
	}
	p.load = func(ctx context.Context, ref PageRef, limit Limits) (Page, error) {
		loads++
		if ref.Key != "one" || ref.Revision != "v1" || limit.MaxItems != 10 {
			t.Fatal(ref, limit)
		}
		return load(ctx, ref, limit)
	}
	allowed := true
	h := newHandlerTest(t, p, HandlerOptions{Authorize: func(_ context.Context, section string) error {
		grants++
		if section != "news" {
			t.Fatal(section)
		}
		if !allowed {
			return ErrNotPublic
		}
		return nil
	}})
	index := sitemapRequest(h, "GET", "/sitemap.xml", "")
	if index.Code != 200 || !strings.Contains(index.Body.String(), "https://example.test/sitemap-news.one.xml") || loads != 0 || index.Header().Get("ETag") == "" {
		t.Fatal(index.Code, index.Body.String(), loads)
	}
	page := sitemapRequest(h, "GET", "/sitemap-news.one.xml", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "https://example.test/articles/one") || page.Header().Get("Cache-Control") != "private, no-cache" || page.Header().Get("Last-Modified") != "" {
		t.Fatal(page.Code, page.Header(), page.Body.String())
	}
	priorGrants := grants
	for _, method := range []string{"GET", "HEAD"} {
		w := sitemapRequest(h, method, "/sitemap-news.one.xml", "W/"+page.Header().Get("ETag"))
		if w.Code != 304 || w.Body.Len() != 0 {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
	if manifests != 4 || loads != 3 || grants != priorGrants+6 {
		t.Fatal("conditional response skipped current checks", manifests, loads, grants)
	}
	allowed = false
	w := sitemapRequest(h, "GET", "/sitemap-news.one.xml", page.Header().Get("ETag"))
	if w.Code != 403 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" || loads != 3 {
		t.Fatal(w.Code, w.Header(), loads)
	}
}

func TestSitemapHTTPMissingSelectorsAndTransportDoNotReadPages(t *testing.T) {
	p := handlerProvider()
	calls := 0
	p.load = func(context.Context, PageRef, Limits) (Page, error) { calls++; return Page{}, nil }
	h := newHandlerTest(t, p, HandlerOptions{})
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", "/sitemap.xml", 405}, {"GET", "/sitemap.xml?", 400}, {"GET", "/sitemap.xml?p=1&p=2", 400}, {"GET", "/sitemap-missing.one.xml", 404}, {"GET", "/sitemap-news.absent.xml", 404}, {"GET", "/sitemap-news.bad-key.xml", 404}, {"GET", "/sitemap-invalid-section.one.xml", 400}} {
		w := sitemapRequest(h, tc.method, tc.path, "")
		if w.Code != tc.status || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(tc, w.Code, w.Header(), w.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "https://example.test/sitemap.xml", nil)
	body := &neverReadBody{}
	req.Body = body
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || body.reads != 0 || calls != 0 {
		t.Fatal("transport or unknown page invoked load", w.Code, body.reads, calls)
	}
}

type neverReadBody struct{ reads int }

func (b *neverReadBody) Read([]byte) (int, error) { b.reads++; return 0, io.ErrClosedPipe }
func (*neverReadBody) Close() error               { return nil }

func TestSitemapHTTPRefusesInvalidStaleAndFailedProviderOutput(t *testing.T) {
	for _, mode := range []string{"duplicate", "overflow", "revision", "missing_revision", "metadata", "mutate_ref", "load_error", "manifest_error", "panic", "denied_entry", "late_cancel"} {
		t.Run(mode, func(t *testing.T) {
			p := handlerProvider()
			cfg := handlerConfig()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "duplicate":
				p.manifest = func(context.Context, int) ([]PageRef, error) {
					return []PageRef{{Key: "one", Revision: "v1"}, {Key: "one", Revision: "v2"}}, nil
				}
			case "overflow":
				p.manifest = func(_ context.Context, limit int) ([]PageRef, error) { return make([]PageRef, limit), nil }
			case "missing_revision":
				p.manifest = func(context.Context, int) ([]PageRef, error) { return []PageRef{{Key: "one"}}, nil }
			case "manifest_error":
				p.manifest = func(context.Context, int) ([]PageRef, error) { return nil, errors.New("private catalog detail") }
			case "denied_entry":
				cfg.Policy.Entry = func(context.Context, Entry) error { return ErrNotPublic }
			default:
				p.load = func(_ context.Context, ref PageRef, _ Limits) (Page, error) {
					page := Page{Ref: ref, Entries: []Entry{{Loc: "/article"}}}
					switch mode {
					case "revision":
						page.Ref.Revision = "changed"
					case "metadata":
						page.Entries[0].Loc = "https://other.test/private"
					case "mutate_ref":
						*ref.LastMod.Time.Location() = *time.FixedZone("changed", 3600)
					case "load_error":
						return Page{}, errors.New("private catalog detail")
					case "panic":
						panic("private catalog detail")
					case "late_cancel":
						cancel()
					}
					return page, nil
				}
			}
			r, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			h, err := r.Handler([]Section{{Name: "news", Provider: p}}, HandlerOptions{Authorize: allowSection})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/sitemap-news.one.xml", nil).WithContext(ctx))
			want := 503
			if mode == "denied_entry" {
				want = 403
			}
			if w.Code != want || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "<urlset") {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
		})
	}
}

func TestSitemapHTTPRegistrationSnapshotsAndDirectory(t *testing.T) {
	cfg := handlerConfig()
	cfg.Directory = "/public/"
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := handlerProvider()
	p.load = func(_ context.Context, ref PageRef, _ Limits) (Page, error) {
		return Page{Ref: ref, Entries: []Entry{{Loc: "/public/article"}}}, nil
	}
	sections := []Section{{Name: "news", Provider: p}}
	h, err := r.Handler(sections, HandlerOptions{Authorize: allowSection, PublicCache: true})
	if err != nil {
		t.Fatal(err)
	}
	sections[0] = Section{Name: "private", Provider: (*testProvider)(nil)}
	*r = Renderer{}
	w := sitemapRequest(h, "GET", "/public/sitemap.xml", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://example.test/public/sitemap-news.one.xml") || w.Header().Get("Cache-Control") != "public, max-age=0, must-revalidate" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	w = sitemapRequest(h, "HEAD", "/public/sitemap-news.one.xml", "")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}

func TestSitemapHTTPFreezesVerifiedPageBeforeFinalAuthorization(t *testing.T) {
	p := handlerProvider()
	entries := []Entry{{Loc: "/original"}}
	p.load = func(_ context.Context, ref PageRef, _ Limits) (Page, error) {
		return Page{Ref: ref, Entries: entries}, nil
	}
	grants := 0
	h := newHandlerTest(t, p, HandlerOptions{Authorize: func(context.Context, string) error {
		grants++
		if grants == 3 {
			entries[0].Loc = "/rewritten-after-revision-check"
		}
		return nil
	}})
	w := sitemapRequest(h, "GET", "/sitemap-news.one.xml", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://example.test/original") || strings.Contains(w.Body.String(), "rewritten") {
		t.Fatal("authorization callback rewrote verified provider output", w.Code, w.Body.String())
	}
}

func TestSitemapHTTPValidatesTemporalMetadataBeforeSnapshotNormalization(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, 1, 2, 3, 0, 0, 0, time.FixedZone("invalid-offset", 86400)),
		time.Date(0, 12, 31, 23, 30, 0, 0, time.FixedZone("invalid-civil-year", -3600)),
	} {
		p := handlerProvider()
		p.load = func(_ context.Context, ref PageRef, _ Limits) (Page, error) {
			return Page{Ref: ref, Entries: []Entry{{Loc: "/article", LastMod: LastModified{Time: at}}}}, nil
		}
		h := newHandlerTest(t, p, HandlerOptions{})
		w := sitemapRequest(h, "GET", "/sitemap-news.one.xml", "")
		if w.Code != 503 || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "<urlset") {
			t.Fatal("normalization hid invalid source time", at, w.Code, w.Body.String())
		}
	}
}

func TestSitemapHTTPRejectsInvalidRegistrationAndAbortsBrokenWrites(t *testing.T) {
	r, err := New(handlerConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, sections := range [][]Section{nil, {{Name: "news", Provider: (*testProvider)(nil)}}, {{Name: "bad-name", Provider: handlerProvider()}}, {{Name: "news", Provider: handlerProvider()}, {Name: "news", Provider: handlerProvider()}}} {
		if _, err := r.Handler(sections, HandlerOptions{Authorize: allowSection}); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
	for _, options := range []HandlerOptions{{}, {Authorize: allowSection, Timeout: time.Nanosecond}, {Authorize: allowSection, Timeout: 6 * time.Minute}} {
		if _, err := r.Handler([]Section{{Name: "news", Provider: handlerProvider()}}, options); err == nil {
			t.Fatal("invalid handler options accepted")
		}
	}
	h := newHandlerTest(t, handlerProvider(), HandlerOptions{})
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatal("broken response stream did not abort", recovered)
		}
	}()
	h.ServeHTTP(&failedSitemapWriter{headers: http.Header{}}, httptest.NewRequest("GET", "https://example.test/sitemap.xml", nil))
}

type failedSitemapWriter struct{ headers http.Header }

func (w *failedSitemapWriter) Header() http.Header     { return w.headers }
func (*failedSitemapWriter) WriteHeader(int)           {}
func (*failedSitemapWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
