package http

import (
	"bytes"
	"compress/gzip"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/cache"
	"github.com/Newton-School/gogo/i18n"
	"github.com/Newton-School/gogo/security"
)

func TestCommonMiddlewareRedirectsAppendSlashAndPrependWWW(t *testing.T) {
	final := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(204)
	})

	appendSlash := Chain(final, CommonMiddleware(CommonMiddlewareOptions{AppendSlash: true}))
	recorder := httptest.NewRecorder()
	appendSlash.ServeHTTP(recorder, httptest.NewRequest("GET", "http://example.com/articles?x=1", nil))
	if recorder.Code != 301 {
		t.Fatalf("append slash status = %d, want 301", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "/articles/?x=1" {
		t.Fatalf("append slash Location = %q, want /articles/?x=1", got)
	}

	prependWWW := Chain(final, CommonMiddleware(CommonMiddlewareOptions{PrependWWW: true}))
	recorder = httptest.NewRecorder()
	prependWWW.ServeHTTP(recorder, httptest.NewRequest("GET", "http://example.com/articles/", nil))
	if recorder.Code != 301 {
		t.Fatalf("prepend www status = %d, want 301", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "http://www.example.com/articles/" {
		t.Fatalf("prepend www Location = %q, want http://www.example.com/articles/", got)
	}
}

func TestCommonMiddlewareReportsBrokenLinksAndDeniesUserAgents(t *testing.T) {
	var reported string
	common := CommonMiddleware(CommonMiddlewareOptions{
		BrokenLinkReporter: func(request *nethttp.Request, status int) {
			reported = request.URL.Path + ":" + nethttp.StatusText(status)
		},
		DeniedUserAgents: []string{"BadBot"},
	})

	notFound := Chain(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		nethttp.NotFound(w, nil)
	}), common)
	notFound.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/missing/", nil))
	if reported != "/missing/:Not Found" {
		t.Fatalf("reported = %q, want /missing/:Not Found", reported)
	}

	deniedRequest := httptest.NewRequest("GET", "/", nil)
	deniedRequest.Header.Set("User-Agent", "BadBot/1.0")
	recorder := httptest.NewRecorder()
	notFound.ServeHTTP(recorder, deniedRequest)
	if recorder.Code != 403 {
		t.Fatalf("denied status = %d, want 403", recorder.Code)
	}
}

func TestConditionalGetMiddlewareReturnsNotModified(t *testing.T) {
	lastModified := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	final := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", lastModified.Format(nethttp.TimeFormat))
		_, _ = w.Write([]byte("body"))
	})

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("If-None-Match", `"abc"`)
	recorder := httptest.NewRecorder()
	Chain(final, ConditionalGetMiddleware()).ServeHTTP(recorder, request)

	if recorder.Code != 304 {
		t.Fatalf("status = %d, want 304", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body length = %d, want 0", recorder.Body.Len())
	}
}

func TestGZipMiddlewareCompressesAndRemovesContentLength(t *testing.T) {
	final := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Length", "11")
		_, _ = w.Write([]byte("hello world"))
	})
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()

	Chain(final, GZipMiddleware()).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := recorder.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want empty", got)
	}
	reader, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader error = %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gzip body read error = %v", err)
	}
	if string(body) != "hello world" {
		t.Fatalf("decompressed body = %q, want hello world", body)
	}
}

func TestLocaleMiddlewareActivatesLanguage(t *testing.T) {
	var seen string
	final := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		seen = i18n.LanguageFromContext(r.Context())
		w.WriteHeader(204)
	})
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept-Language", "fr-CA,fr;q=0.9,en;q=0.8")

	Chain(final, LocaleMiddleware(LocaleOptions{Supported: []string{"en", "fr"}, Default: "en"})).ServeHTTP(httptest.NewRecorder(), request)

	if seen != "fr" {
		t.Fatalf("language = %q, want fr", seen)
	}
}

func TestCacheMiddlewareFetchesAndUpdatesCache(t *testing.T) {
	store := cache.NewMemoryStore()
	count := 0
	final := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		count++
		w.Header().Set("X-Source", "handler")
		_, _ = w.Write([]byte("cached response"))
	})
	handler := Chain(final, CacheMiddleware(CacheOptions{Store: store, TTL: time.Minute}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest("GET", "/items/", nil))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest("GET", "/items/", nil))

	if count != 1 {
		t.Fatalf("handler count = %d, want 1", count)
	}
	if second.Body.String() != "cached response" || second.Header().Get("X-Source") != "handler" {
		t.Fatalf("cached response = (%q, %q), want body and header", second.Body.String(), second.Header().Get("X-Source"))
	}
}

func TestClickjackingMiddlewareSetsSecurityHeader(t *testing.T) {
	final := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(204)
	})

	recorder := httptest.NewRecorder()
	Chain(final, ClickjackingMiddleware(security.FrameDeny)).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))

	if got := recorder.Header().Get(security.HeaderXFrameOptions); got != security.FrameDeny {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
}
