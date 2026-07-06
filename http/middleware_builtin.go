package http

import (
	"bytes"
	"compress/gzip"
	"io"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Newton-School/gogo/cache"
	"github.com/Newton-School/gogo/i18n"
	"github.com/Newton-School/gogo/security"
)

// CommonMiddlewareOptions configures Django-style common middleware behavior.
type CommonMiddlewareOptions struct {
	AppendSlash        bool
	PrependWWW         bool
	BrokenLinkReporter func(*nethttp.Request, int)
	DeniedUserAgents   []string
}

// CommonMiddleware handles append-slash, prepend-www, broken-link reporting, and user-agent denial.
func CommonMiddleware(options CommonMiddlewareOptions) Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			if deniedUserAgent(r.Header.Get("User-Agent"), options.DeniedUserAgents) {
				nethttp.Error(w, "Forbidden", nethttp.StatusForbidden)
				return
			}
			if location := commonRedirectLocation(r, options); location != "" {
				nethttp.Redirect(w, r, location, nethttp.StatusMovedPermanently)
				return
			}

			recorder := &statusRecorder{ResponseWriter: w, status: nethttp.StatusOK}
			next.ServeHTTP(recorder, r)
			if recorder.status == nethttp.StatusNotFound && options.BrokenLinkReporter != nil {
				options.BrokenLinkReporter(r, recorder.status)
			}
		})
	}
}

// ConditionalGetMiddleware converts matching ETag/Last-Modified responses to 304.
func ConditionalGetMiddleware() Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			buffer := newBufferedResponse()
			next.ServeHTTP(buffer, r)

			if conditionalMiddlewareNotModified(r, buffer.header) {
				copyHeader(w.Header(), conditionalHeaders(buffer.header))
				w.WriteHeader(nethttp.StatusNotModified)
				return
			}
			buffer.writeTo(w)
		})
	}
}

// GZipMiddleware compresses eligible responses when the client accepts gzip.
func GZipMiddleware() Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			buffer := newBufferedResponse()
			next.ServeHTTP(buffer, r)

			if !acceptsGZip(NewRequest(r)) || !gzipEligible(buffer) {
				buffer.writeTo(w)
				return
			}

			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err := writer.Write(buffer.body.Bytes()); err != nil {
				_ = writer.Close()
				nethttp.Error(w, "Internal Server Error", nethttp.StatusInternalServerError)
				return
			}
			if err := writer.Close(); err != nil {
				nethttp.Error(w, "Internal Server Error", nethttp.StatusInternalServerError)
				return
			}

			buffer.body.Reset()
			_, _ = buffer.body.Write(compressed.Bytes())
			buffer.header.Del("Content-Length")
			buffer.header.Set("Content-Encoding", "gzip")
			appendVary(buffer.header, "Accept-Encoding")
			buffer.writeTo(w)
		})
	}
}

// LocaleOptions configures locale activation middleware.
type LocaleOptions struct {
	Supported []string
	Default   string
}

// LocaleMiddleware stores the negotiated language in request context.
func LocaleMiddleware(options LocaleOptions) Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			language := i18n.NegotiateLanguage(r.Header.Get("Accept-Language"), options.Supported, options.Default)
			next.ServeHTTP(w, r.WithContext(i18n.WithLanguage(r.Context(), language)))
		})
	}
}

// CacheOptions configures page cache middleware.
type CacheOptions struct {
	Store   cache.Store
	TTL     time.Duration
	KeyFunc func(*nethttp.Request) string
}

// CacheMiddleware fetches cacheable responses before the handler and updates cache after misses.
func CacheMiddleware(options CacheOptions) Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			if options.Store == nil || !cacheableRequest(r) {
				next.ServeHTTP(w, r)
				return
			}

			key := cacheKey(r, options.KeyFunc)
			if entry, ok, err := options.Store.Get(r.Context(), key); err == nil && ok {
				writeCacheEntry(w, entry)
				return
			}

			buffer := newBufferedResponse()
			next.ServeHTTP(buffer, r)
			buffer.writeTo(w)
			if buffer.status == nethttp.StatusOK && buffer.header.Get("Set-Cookie") == "" {
				_ = options.Store.Set(r.Context(), key, cache.Entry{
					Status: buffer.status,
					Header: buffer.header.Clone(),
					Body:   append([]byte(nil), buffer.body.Bytes()...),
				}, options.TTL)
			}
		})
	}
}

// ClickjackingMiddleware applies X-Frame-Options.
func ClickjackingMiddleware(option string) Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			security.ApplyFrameOptions(w.Header(), option)
			next.ServeHTTP(w, r)
		})
	}
}

type bufferedResponse struct {
	header nethttp.Header
	body   bytes.Buffer
	status int
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(nethttp.Header), status: nethttp.StatusOK}
}

func (r *bufferedResponse) Header() nethttp.Header {
	return r.header
}

func (r *bufferedResponse) WriteHeader(status int) {
	r.status = status
}

func (r *bufferedResponse) Write(data []byte) (int, error) {
	return r.body.Write(data)
}

func (r *bufferedResponse) writeTo(w nethttp.ResponseWriter) {
	copyHeader(w.Header(), r.header)
	w.WriteHeader(r.status)
	if r.body.Len() > 0 {
		_, _ = w.Write(r.body.Bytes())
	}
}

func deniedUserAgent(userAgent string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(userAgent, pattern) {
			return true
		}
	}
	return false
}

func commonRedirectLocation(r *nethttp.Request, options CommonMiddlewareOptions) string {
	host := r.Host
	path := r.URL.Path
	changedHost := false
	changedPath := false

	if options.PrependWWW && host != "" && !strings.HasPrefix(strings.ToLower(host), "www.") {
		host = "www." + host
		changedHost = true
	}
	if options.AppendSlash && path != "/" && !strings.HasSuffix(path, "/") {
		path += "/"
		changedPath = true
	}
	if !changedHost && !changedPath {
		return ""
	}
	if changedHost {
		target := url.URL{Scheme: requestScheme(r), Host: host, Path: path, RawQuery: r.URL.RawQuery}
		return target.String()
	}
	target := path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return target
}

func requestScheme(r *nethttp.Request) string {
	if r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func conditionalMiddlewareNotModified(r *nethttp.Request, header nethttp.Header) bool {
	if r.Method != nethttp.MethodGet && r.Method != nethttp.MethodHead {
		return false
	}

	var lastModified time.Time
	if value := header.Get("Last-Modified"); value != "" {
		parsed, err := nethttp.ParseTime(value)
		if err == nil {
			lastModified = parsed
		}
	}
	return requestNotModified(NewRequest(r), header.Get("ETag"), lastModified)
}

func conditionalHeaders(header nethttp.Header) nethttp.Header {
	next := make(nethttp.Header)
	for _, name := range []string{"ETag", "Last-Modified", "Cache-Control", "Expires", "Vary"} {
		if value := header.Values(name); len(value) > 0 {
			next[name] = append([]string(nil), value...)
		}
	}
	return next
}

func gzipEligible(buffer *bufferedResponse) bool {
	if buffer.status < 200 || buffer.status == nethttp.StatusNoContent || buffer.status == nethttp.StatusNotModified {
		return false
	}
	return buffer.body.Len() > 0 && buffer.header.Get("Content-Encoding") == ""
}

func cacheableRequest(r *nethttp.Request) bool {
	return r.Method == nethttp.MethodGet || r.Method == nethttp.MethodHead
}

func cacheKey(r *nethttp.Request, keyFunc func(*nethttp.Request) string) string {
	if keyFunc != nil {
		return keyFunc(r)
	}
	return r.Method + ":" + r.Host + ":" + r.URL.RequestURI()
}

func writeCacheEntry(w nethttp.ResponseWriter, entry cache.Entry) {
	copyHeader(w.Header(), entry.Header)
	status := entry.Status
	if status == 0 {
		status = nethttp.StatusOK
	}
	w.WriteHeader(status)
	if len(entry.Body) > 0 {
		_, _ = w.Write(entry.Body)
	}
}

func copyHeader(dst, src nethttp.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

var _ nethttp.ResponseWriter = (*bufferedResponse)(nil)
var _ io.Writer = (*bufferedResponse)(nil)
