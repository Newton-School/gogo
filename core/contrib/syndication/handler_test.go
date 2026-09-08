package syndication

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

func publicFeed(context.Context) error { return nil }

func TestFeedHandlerGetHeadAndValidatorsReauthorize(t *testing.T) {
	cfg := renderConfig()
	cfg.MaxItems = 10
	allowed := true
	itemCalls, sourceCalls, feedCalls := 0, 0, 0
	cfg.Policy.Item = func(context.Context, Item) error {
		itemCalls++
		if !allowed {
			return ErrNotPublic
		}
		return nil
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.Handler(func(_ context.Context, limit int) (Feed, error) {
		sourceCalls++
		if limit != 11 {
			t.Fatal(limit)
		}
		return sampleFeed(), nil
	}, HandlerOptions{Authorize: func(context.Context) error { feedCalls++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest("GET", "https://example.test/feed.xml", nil))
	if get.Code != 200 || get.Header().Get("Content-Type") != "application/rss+xml; charset=utf-8" || get.Header().Get("ETag") == "" || get.Header().Get("Cache-Control") != "private, no-cache" || get.Header().Get("Last-Modified") != "" || get.Body.Len() == 0 {
		t.Fatal(get.Code, get.Header(), get.Body.String())
	}
	for _, method := range []string{"GET", "HEAD"} {
		req := httptest.NewRequest(method, "https://example.test/feed.xml", nil)
		req.Header.Set("If-None-Match", "W/"+get.Header().Get("ETag"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 304 || w.Body.Len() != 0 {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
	if itemCalls != 3 || sourceCalls != 3 || feedCalls != 3 {
		t.Fatal("cached response skipped current policy", itemCalls, sourceCalls, feedCalls)
	}
	allowed = false
	req := httptest.NewRequest("GET", "https://example.test/feed.xml", nil)
	req.Header.Set("If-None-Match", get.Header().Get("ETag"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 || strings.Contains(w.Body.String(), "Public updates") {
		t.Fatal("conditional denial leaked cached feed", w.Code, w.Body.String())
	}
	allowed = true
	req = httptest.NewRequest("GET", "https://example.test/feed.xml", nil)
	req.Header.Set("If-Modified-Since", time.Now().Add(100*365*24*time.Hour).UTC().Format(http.TimeFormat))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal("weak item timestamp produced false 304", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("HEAD", "https://example.test/feed.xml", nil))
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatal(w.Code, w.Header())
	}
}

type unreadableBody struct{ reads int }

func (b *unreadableBody) Read([]byte) (int, error) { b.reads++; return 0, errors.New("must not read") }
func (*unreadableBody) Close() error               { return nil }

func TestFeedHandlerTransportAndSourceFailuresReturnNoFeed(t *testing.T) {
	r, _ := New(renderConfig())
	calls := 0
	h, _ := r.Handler(func(context.Context, int) (Feed, error) { calls++; return sampleFeed(), nil }, HandlerOptions{Authorize: publicFeed})
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"POST", "/feed.xml", 405}, {"GET", "/feed.xml?x=1", 400}, {"GET", "/feed.xml?", 400}, {"OPTIONS", "/feed.xml", 405}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "https://example.test"+tc.path, nil))
		if w.Code != tc.want || w.Body.Len() != 0 {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "https://example.test/feed.xml", nil)
	body := &unreadableBody{}
	req.Body = body
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || body.reads != 0 || calls != 0 {
		t.Fatal("transport failure invoked source/body", w.Code, body.reads, calls)
	}
	for _, source := range []Source{
		func(context.Context, int) (Feed, error) { return sampleFeed(), errors.New("private database detail") },
		func(context.Context, int) (Feed, error) { panic("private database detail") },
		func(context.Context, int) (Feed, error) { f := sampleFeed(); f.Title = ""; return f, nil },
		func(_ context.Context, limit int) (Feed, error) {
			f := sampleFeed()
			f.Items = make([]Item, limit)
			return f, nil
		},
	} {
		h, err := r.Handler(source, HandlerOptions{Authorize: publicFeed})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/feed.xml", nil))
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "<rss") || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
	}
	calls = 0
	h, _ = r.Handler(func(context.Context, int) (Feed, error) { calls++; return sampleFeed(), nil }, HandlerOptions{Authorize: func(context.Context) error { return ErrNotPublic }})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/feed.xml", nil))
	if w.Code != 403 || calls != 0 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(w.Code, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h, _ = r.Handler(func(context.Context, int) (Feed, error) { cancel(); return sampleFeed(), nil }, HandlerOptions{Authorize: publicFeed})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/feed.xml", nil).WithContext(ctx))
	if w.Code != 503 || strings.Contains(w.Body.String(), "<rss") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestFeedHandlerSnapshotsRegistrationAndPropagatesWriteFailure(t *testing.T) {
	r, _ := New(renderConfig())
	h, err := r.Handler(func(context.Context, int) (Feed, error) { return sampleFeed(), nil }, HandlerOptions{Authorize: publicFeed, PublicCache: true})
	if err != nil {
		t.Fatal(err)
	}
	other := renderConfig()
	other.Policy.Item = func(context.Context, Item) error { return ErrNotPublic }
	replacement, _ := New(other)
	*r = *replacement
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/feed.xml", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=0, must-revalidate" {
		t.Fatal("registration borrowed mutable renderer", w.Code, w.Header())
	}
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatal("partial writer did not abort the stream", got)
		}
	}()
	h.ServeHTTP(&brokenFeedWriter{header: http.Header{}}, httptest.NewRequest("GET", "https://example.test/feed.xml", nil))
}

type brokenFeedWriter struct{ header http.Header }

func (w *brokenFeedWriter) Header() http.Header     { return w.header }
func (*brokenFeedWriter) WriteHeader(int)           {}
func (*brokenFeedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
