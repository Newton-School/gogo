package files

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
)

type downloadReader struct {
	*bytes.Reader
	reads, seeks, closes, maxRead int
	readHook                      func([]byte) (int, error)
	seekHook                      func(int64, int) (int64, error)
	closeHook                     func() error
}

func (r *downloadReader) Read(p []byte) (int, error) {
	r.reads++
	r.maxRead = max(r.maxRead, len(p))
	if r.readHook != nil {
		return r.readHook(p)
	}
	return r.Reader.Read(p)
}
func (r *downloadReader) Seek(n int64, whence int) (int64, error) {
	r.seeks++
	if r.seekHook != nil {
		return r.seekHook(n, whence)
	}
	return r.Reader.Seek(n, whence)
}
func (r *downloadReader) Close() error {
	r.closes++
	if r.closeHook != nil {
		return r.closeHook()
	}
	return nil
}

type downloadFixture struct {
	s       *Service
	b       *fileServiceBackend
	storage *fileServiceStorage
	info    Info
	reader  *downloadReader
	options DownloadOptions
	reads   *int
}

func newDownloadFixture(t *testing.T, body string) *downloadFixture {
	t.Helper()
	config, backend, storage := fileServiceConfig(t)
	grants := new(int)
	config.Bindings[0].Authorize = func(_ context.Context, action OwnerAction, _ OwnerSnapshot) error {
		if action == ReadFile {
			*grants++
		}
		return nil
	}
	s := mustFileService(t, config)
	info, err := s.StoreValidated(context.Background(), fileServiceInput(t), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	f := &downloadFixture{s: s, b: backend, storage: storage, info: info, reads: grants}
	storage.openHook = func(context.Context, string) (Reader, error) {
		f.reader = &downloadReader{Reader: bytes.NewReader([]byte(body))}
		return f.reader, nil
	}
	f.options = DownloadOptions{ID: func(*http.Request) (string, error) { return info.ID, nil }, Authorize: func(*http.Request) error { return nil }}
	return f
}

func (f *downloadFixture) handler(t *testing.T) http.Handler {
	t.Helper()
	h, err := NewDownloadHandler(f.s, f.options)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func downloadRequest(method string, header http.Header) *http.Request {
	r := httptest.NewRequest(method, "https://example.test/private/file?version=one", nil)
	if header != nil {
		r.Header = header
	}
	return r
}

func TestDownloadHandlerConstruction(t *testing.T) {
	f := newDownloadFixture(t, "private")
	for _, name := range []string{"", "report.csv", "résumé.txt", `quote".txt`} {
		opts := f.options
		opts.Filename = name
		h, err := NewDownloadHandler(f.s, opts)
		if err != nil || h == nil {
			t.Fatal(name, err)
		}
	}
	for _, name := range []string{"..", " ", ". .", "a/b", `a\b`, "a\n", "\x00", string([]byte{0xff}), strings.Repeat("a", 256)} {
		opts := f.options
		opts.Filename = name
		if _, err := NewDownloadHandler(f.s, opts); err != ErrConfiguration {
			t.Fatal(name, err)
		}
	}
	for _, duration := range []time.Duration{-1, time.Microsecond, 5*time.Minute + 1} {
		opts := f.options
		opts.Timeout = duration
		if _, err := NewDownloadHandler(f.s, opts); err != ErrConfiguration {
			t.Fatal(duration, err)
		}
	}
	for _, mode := range []string{"nil-service", "zero-service", "id", "authorize"} {
		opts, s := f.options, f.s
		switch mode {
		case "nil-service":
			s = nil
		case "zero-service":
			s = &Service{}
		case "id":
			opts.ID = nil
		case "authorize":
			opts.Authorize = nil
		}
		if _, err := NewDownloadHandler(s, opts); err != ErrConfiguration {
			t.Fatal(mode, err)
		}
	}
	if f.storage.opens != 0 || *f.reads != 0 {
		t.Fatal("constructor touched storage/authority")
	}
}

func TestDownloadHandlerRepresentationAndConditionals(t *testing.T) {
	f := newDownloadFixture(t, "0123456789")
	f.options.Filename = "résumé.txt"
	h := f.handler(t)
	initial := httptest.NewRecorder()
	h.ServeHTTP(initial, downloadRequest("GET", nil))
	if initial.Code != 200 || initial.Body.String() != "0123456789" || f.reader.closes != 1 || *f.reads != 2 {
		t.Fatal(initial.Code, initial.Body.String(), f.reader, *f.reads)
	}
	head := initial.Header()
	kind, params, err := mime.ParseMediaType(head.Get("Content-Disposition"))
	if err != nil || kind != "attachment" || params["filename"] != "résumé.txt" || head.Get("Cache-Control") != "private, no-store" || head.Get("X-Content-Type-Options") != "nosniff" || head.Get("Content-Length") != "10" || head.Get("Content-Type") != "text/plain" {
		t.Fatal(head, err)
	}
	etag, modified := head.Get("Etag"), head.Get("Last-Modified")
	if etag == "" || strings.Contains(etag, f.info.ID) || strings.Contains(etag, f.info.Checksum) {
		t.Fatal("unusable or revealing validator", etag)
	}
	for _, tc := range []struct {
		name, method string
		header       http.Header
		status       int
		body, length string
	}{
		{"head", "HEAD", nil, 200, "", "10"},
		{"head-ignores-range", "HEAD", http.Header{"Range": {"bytes=1-2"}}, 200, "", "10"},
		{"weak-inm", "GET", http.Header{"If-None-Match": {"W/" + etag}}, 304, "", ""},
		{"multiline-inm", "GET", http.Header{"If-None-Match": {`"other"`, etag}}, 304, "", ""},
		{"empty-list-elements", "GET", http.Header{"If-None-Match": {", " + etag + ", ,"}}, 304, "", ""},
		{"inm-star", "HEAD", http.Header{"If-None-Match": {"*"}}, 304, "", ""},
		{"invalid-inm-suppresses-date", "GET", http.Header{"If-None-Match": {etag + ", bad"}, "If-Modified-Since": {modified}}, 200, "0123456789", "10"},
		{"empty-present-inm-suppresses-date", "GET", http.Header{"If-None-Match": nil, "If-Modified-Since": {modified}}, 200, "0123456789", "10"},
		{"date", "GET", http.Header{"If-Modified-Since": {modified}}, 304, "", ""},
		{"bad-date", "GET", http.Header{"If-Modified-Since": {"bad"}}, 200, "0123456789", "10"},
		{"ifmatch-strong", "GET", http.Header{"If-Match": {etag}, "If-Unmodified-Since": {"Sat, 01 Jan 2000 00:00:00 GMT"}}, 200, "0123456789", "10"},
		{"ifmatch-weak", "GET", http.Header{"If-Match": {"W/" + etag}}, 412, "", "0"},
		{"ifmatch-priority", "GET", http.Header{"If-Match": {`"wrong"`}, "If-None-Match": {etag}}, 412, "", "0"},
		{"ifmatch-malformed", "GET", http.Header{"If-Match": {etag + ", nope"}}, 412, "", "0"},
		{"unmodified", "GET", http.Header{"If-Unmodified-Since": {"Sat, 01 Jan 2000 00:00:00 GMT"}}, 412, "", "0"},
		{"range", "GET", http.Header{"Range": {"bytes=2-4"}}, 206, "234", "3"},
		{"mixed-case-unit", "GET", http.Header{"Range": {"Bytes=2-4"}}, 206, "234", "3"},
		{"uppercase-unit", "GET", http.Header{"Range": {"BYTES=2-4"}}, 206, "234", "3"},
		{"open-range", "GET", http.Header{"Range": {"bytes=7-"}}, 206, "789", "3"},
		{"suffix", "GET", http.Header{"Range": {"bytes=-3"}}, 206, "789", "3"},
		{"clamp", "GET", http.Header{"Range": {"bytes=8-999"}}, 206, "89", "2"},
		{"multi-ignore", "GET", http.Header{"Range": {"bytes=0-1,4-5"}}, 200, "0123456789", "10"},
		{"unknown-ignore", "GET", http.Header{"Range": {"items=0-1"}}, 200, "0123456789", "10"},
		{"empty-range", "GET", http.Header{"Range": {""}}, 416, "", "0"},
		{"unsatisfiable", "GET", http.Header{"Range": {"bytes=10-"}}, 416, "", "0"},
		{"invalid", "GET", http.Header{"Range": {"bytes=3-2"}}, 416, "", "0"},
		{"ifrange-tag", "GET", http.Header{"Range": {"bytes=2-4"}, "If-Range": {etag}}, 206, "234", "3"},
		{"ifrange-weak", "GET", http.Header{"Range": {"bytes=2-4"}, "If-Range": {"W/" + etag}}, 200, "0123456789", "10"},
		{"ifrange-empty", "GET", http.Header{"Range": {"bytes=2-4"}, "If-Range": {""}}, 200, "0123456789", "10"},
		{"ifrange-whitespace", "GET", http.Header{"Range": {"bytes=2-4"}, "If-Range": {" \t"}}, 200, "0123456789", "10"},
		{"ifrange-date", "GET", http.Header{"Range": {"bytes=2-4"}, "If-Range": {modified}}, 206, "234", "3"},
		{"ifrange-later-date", "GET", http.Header{"Range": {"bytes=2-4"}, "If-Range": {"Fri, 31 Dec 9999 00:00:00 GMT"}}, 200, "0123456789", "10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := *f.reads
			out := httptest.NewRecorder()
			h.ServeHTTP(out, downloadRequest(tc.method, tc.header))
			if out.Code != tc.status || out.Body.String() != tc.body || out.Header().Get("Content-Length") != tc.length || f.reader.closes != 1 || *f.reads != before+2 {
				t.Fatal(out.Code, out.Body.String(), out.Header(), f.reader, *f.reads-before)
			}
			if (tc.method == "HEAD" || tc.status == 304 || tc.status == 412 || tc.status == 416) && f.reader.reads != 0 {
				t.Fatal("body read for body-free outcome", f.reader.reads)
			}
			if tc.status == 416 && out.Header().Get("Content-Range") != "bytes */10" {
				t.Fatal(out.Header())
			}
		})
	}
}

type downloadMutationContext struct {
	context.Context
	hook func()
}

func (c *downloadMutationContext) Err() error {
	if c.hook != nil {
		hook := c.hook
		c.hook = nil
		hook()
	}
	return c.Context.Err()
}

type downloadBodyTripwire struct{ calls int }

func (b *downloadBodyTripwire) Read([]byte) (int, error) { b.calls++; panic("body read") }
func (b *downloadBodyTripwire) Close() error             { b.calls++; panic("body close") }

func TestDownloadHandlerFreezesRequestAndServiceBeforeCallbacks(t *testing.T) {
	f := newDownloadFixture(t, "private")
	request := downloadRequest("HEAD", http.Header{"Cookie": {"session=one"}, "If-None-Match": {`"wrong"`}})
	body := &downloadBodyTripwire{}
	request.Body, request.GetBody = body, func() (io.ReadCloser, error) { panic("get body") }
	request.ContentLength = 999
	request.Form, request.PostForm = url.Values{"secret": {"value"}}, url.Values{"secret": {"value"}}
	request.Trailer = http.Header{"secret": {"value"}}
	request.TransferEncoding = []string{"chunked"}
	request.TLS, request.Response = &tls.ConnectionState{}, &http.Response{}
	request.Cancel = make(chan struct{})
	request.SetPathValue("id", "not retained")
	ctx := &downloadMutationContext{Context: context.Background(), hook: func() {
		request.Method = "GET"
		request.Header.Set("If-None-Match", "*")
		request.URL.Path = "/changed"
	}}
	request = request.WithContext(ctx)
	var calls []string
	assert := func(r *http.Request) {
		if r.Method != "HEAD" || r.Header.Get("Cookie") != "session=one" || r.Header.Get("If-None-Match") != `"wrong"` || r.URL.Path != "/private/file" || r.PathValue("id") != "" {
			t.Fatal("request snapshot changed", r)
		}
		if r.Body != http.NoBody || r.GetBody != nil || r.Form != nil || r.PostForm != nil || r.MultipartForm != nil || r.Trailer != nil || r.Response != nil || r.TLS != nil || r.Cancel != nil || r.TransferEncoding != nil || r.ContentLength != 0 {
			t.Fatal("mutable ancillary request state retained")
		}
		r.Method, r.URL.Path = "POST", "/callback-change"
		r.Header.Set("Cookie", "changed")
		r.Form = url.Values{"changed": {"yes"}}
		*f.s = Service{}
	}
	f.options.Authorize = func(r *http.Request) error { calls = append(calls, "authorize"); assert(r); return nil }
	f.options.ID = func(r *http.Request) (string, error) { calls = append(calls, "id"); assert(r); return f.info.ID, nil }
	h := f.handler(t)
	f.options.Authorize = func(*http.Request) error { panic("replaced options") }
	out := httptest.NewRecorder()
	h.ServeHTTP(out, request)
	if out.Code != 200 || out.Body.Len() != 0 || f.reader.reads != 0 || body.calls != 0 || !reflect.DeepEqual(calls, []string{"authorize", "id", "authorize"}) {
		t.Fatal(out.Code, calls, f.reader, body.calls)
	}
}

func TestDownloadHandlerRequestBoundsBeforeContextOrGrants(t *testing.T) {
	f := newDownloadFixture(t, "private")
	h := f.handler(t)
	for _, mode := range []string{"headers", "values", "bytes", "key-bytes", "case", "etag-lines", "etag-bytes", "range-lines", "range-bytes", "date-lines", "path", "host", "raw-path", "user", "control"} {
		t.Run(mode, func(t *testing.T) {
			r := downloadRequest("GET", nil)
			switch mode {
			case "headers":
				for i := 0; i < 257; i++ {
					r.Header.Set(strings.Repeat("a", i+1), "x")
				}
			case "values":
				r.Header["A"] = make([]string, 1025)
			case "bytes":
				r.Header.Set("A", strings.Repeat("a", 64<<10))
			case "key-bytes":
				r.Header[strings.Repeat("a", (64<<10)+1)] = nil
			case "case":
				r.Header["If-None-Match"] = []string{"*"}
				r.Header["if-none-match"] = []string{"x"}
			case "etag-lines":
				r.Header["If-Match"] = make([]string, 33)
			case "etag-bytes":
				r.Header.Set("If-None-Match", strings.Repeat("a", 4097))
			case "range-lines":
				r.Header["Range"] = []string{"bytes=0-1", "bytes=2-3"}
			case "range-bytes":
				r.Header.Set("Range", strings.Repeat("a", 129))
			case "date-lines":
				r.Header["If-Modified-Since"] = []string{"one", "two"}
			case "path":
				r.URL.Path = "/" + strings.Repeat("a", 16<<10)
			case "host":
				r.Host = strings.Repeat("a", 1025)
			case "raw-path":
				r.URL.RawPath = "/different"
			case "user":
				r.URL.User = url.User("secret")
			case "control":
				r.Header.Set("X-Test", "a\rb")
			}
			ctxCalls := 0
			r = r.WithContext(&downloadMutationContext{Context: context.Background(), hook: func() { ctxCalls++; panic("context called before bounds") }})
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != 400 || ctxCalls != 0 || f.storage.opens != 0 || *f.reads != 0 {
				t.Fatal(out.Code, ctxCalls, f.storage.opens, *f.reads)
			}
		})
	}
}

func TestDownloadHandlerDenialAndCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"method", "first-denial", "final-denial", "unauthenticated", "mixed-denial", "id-invalid", "id-error", "id-panic", "auth-panic", "cancel", "context-panic", "non-ready", "link-drift", "owner-hidden"} {
		t.Run(mode, func(t *testing.T) {
			f := newDownloadFixture(t, "private")
			want, method, grants := 503, "GET", 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.options.Authorize = func(*http.Request) error { grants++; return nil }
			switch mode {
			case "method":
				want, method = 405, "POST"
			case "first-denial":
				want = 403
				f.options.Authorize = func(*http.Request) error { return ErrForbidden }
			case "final-denial":
				want = 403
				f.options.Authorize = func(*http.Request) error {
					grants++
					if grants == 2 {
						return auth.ErrPermissionDenied
					}
					return nil
				}
			case "unauthenticated":
				want = 401
				f.options.Authorize = func(*http.Request) error { return auth.ErrUnauthenticated }
			case "mixed-denial":
				f.options.Authorize = func(*http.Request) error { return errors.Join(ErrForbidden, errors.New("secret")) }
			case "id-invalid":
				want = 404
				f.options.ID = func(*http.Request) (string, error) { return "../private-key", nil }
			case "id-error":
				f.options.ID = func(*http.Request) (string, error) { return "", errors.New("secret") }
			case "id-panic":
				f.options.ID = func(*http.Request) (string, error) { panic("secret") }
			case "auth-panic":
				f.options.Authorize = func(*http.Request) error { panic("secret") }
			case "cancel":
				f.options.ID = func(*http.Request) (string, error) { cancel(); return f.info.ID, nil }
			case "non-ready":
				want = 404
				f.b.files[f.info.ID][4] = string(Deleting)
			case "link-drift":
				want = 404
				f.b.owner[2] = strings.Repeat("f", 32)
			case "owner-hidden":
				want = 404
				f.b.owner = nil
			}
			r := downloadRequest(method, http.Header{"If-None-Match": {"*"}}).WithContext(ctx)
			if mode == "context-panic" {
				r = r.WithContext(&downloadMutationContext{Context: ctx, hook: func() { panic("secret") }})
			}
			out := httptest.NewRecorder()
			f.handler(t).ServeHTTP(out, r)
			if out.Code != want || f.storage.opens != 0 || strings.Contains(out.Body.String(), "secret") || out.Header().Get("Etag") != "" {
				t.Fatal(mode, out.Code, out.Body.String(), f.storage.opens, out.Header())
			}
			if mode == "method" && (grants != 0 || out.Header().Get("Allow") != "GET, HEAD") {
				t.Fatal(grants, out.Header())
			}
		})
	}
}

type downloadWriter struct {
	*httptest.ResponseRecorder
	headerHook func()
	writeHook  func([]byte) (int, error)
	statusHook func()
}

func (w *downloadWriter) Header() http.Header {
	if w.headerHook != nil {
		w.headerHook()
	}
	return w.ResponseRecorder.Header()
}
func (w *downloadWriter) WriteHeader(n int) {
	if w.statusHook != nil {
		w.statusHook()
	}
	w.ResponseRecorder.WriteHeader(n)
}
func (w *downloadWriter) Write(p []byte) (int, error) {
	if w.writeHook != nil {
		return w.writeHook(p)
	}
	return w.ResponseRecorder.Write(p)
}

func TestDownloadHandlerTransferCompletion(t *testing.T) {
	for _, mode := range []string{"nil", "typed-nil", "seek", "wrong-size", "start-seek", "read", "zero", "negative", "overread", "panic-read", "mixed-eof", "short-late", "error-late", "close-before", "close-after", "close-panic", "short-write", "over-write", "write-error", "write-panic", "header-panic", "cancel-header", "cancel-read", "cancel-write"} {
		t.Run(mode, func(t *testing.T) {
			body := strings.Repeat("private", 20000)
			f := newDownloadFixture(t, body)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := &downloadReader{Reader: bytes.NewReader([]byte(body))}
			f.storage.openHook = func(context.Context, string) (Reader, error) { return reader, nil }
			method, wantAbort := "GET", false
			out := &downloadWriter{ResponseRecorder: httptest.NewRecorder()}
			switch mode {
			case "nil":
				f.storage.openHook = func(context.Context, string) (Reader, error) { return nil, nil }
			case "typed-nil":
				f.storage.openHook = func(context.Context, string) (Reader, error) { var r *downloadReader; return r, nil }
			case "seek":
				reader.seekHook = func(int64, int) (int64, error) { return 0, errors.New("private path") }
			case "wrong-size":
				reader.seekHook = func(int64, int) (int64, error) { return 2, nil }
			case "start-seek":
				reader.seekHook = func(n int64, w int) (int64, error) {
					if w == io.SeekStart {
						return 1, nil
					}
					return reader.Reader.Seek(n, w)
				}
			case "read":
				reader.readHook = func([]byte) (int, error) { return 0, errors.New("private path") }
			case "zero":
				reader.readHook = func([]byte) (int, error) { return 0, nil }
			case "negative":
				reader.readHook = func([]byte) (int, error) { return -1, nil }
			case "overread":
				reader.readHook = func(p []byte) (int, error) { return len(p) + 1, nil }
			case "panic-read":
				reader.readHook = func([]byte) (int, error) { panic("private path") }
			case "mixed-eof":
				reader.readHook = func(p []byte) (int, error) {
					n, _ := reader.Reader.Read(p)
					return n, errors.Join(io.EOF, errors.New("private"))
				}
			case "short-late":
				wantAbort = true
				reader.readHook = func(p []byte) (int, error) {
					if reader.reads > 1 {
						return 0, io.EOF
					}
					return reader.Reader.Read(p)
				}
			case "error-late":
				wantAbort = true
				reader.readHook = func(p []byte) (int, error) {
					if reader.reads > 1 {
						panic("private")
					}
					return reader.Reader.Read(p)
				}
			case "close-before":
				method = "HEAD"
				reader.closeHook = func() error { return errors.New("private") }
			case "close-after":
				wantAbort = true
				reader.closeHook = func() error { return errors.New("private") }
			case "close-panic":
				wantAbort = true
				reader.closeHook = func() error { panic("private") }
			case "short-write":
				wantAbort = true
				out.writeHook = func(p []byte) (int, error) { return len(p) - 1, nil }
			case "over-write":
				wantAbort = true
				out.writeHook = func(p []byte) (int, error) { return len(p) + 1, nil }
			case "write-error":
				wantAbort = true
				out.writeHook = func([]byte) (int, error) { return 0, errors.New("private") }
			case "write-panic":
				wantAbort = true
				out.writeHook = func([]byte) (int, error) { panic("private") }
			case "header-panic":
				wantAbort = true
				out.statusHook = func() { panic("private") }
			case "cancel-header":
				wantAbort = true
				out.statusHook = cancel
			case "cancel-read":
				reader.readHook = func(p []byte) (int, error) { n, err := reader.Reader.Read(p); cancel(); return n, err }
			case "cancel-write":
				wantAbort = true
				out.writeHook = func(p []byte) (int, error) { cancel(); return out.ResponseRecorder.Write(p) }
			}
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				f.handler(t).ServeHTTP(out, downloadRequest(method, nil).WithContext(ctx))
			}()
			if wantAbort {
				if recovered != http.ErrAbortHandler {
					t.Fatal("missing safe abort", recovered, out.Code)
				}
				if strings.Contains(out.Body.String(), "Service Unavailable") {
					t.Fatal("second error response appended")
				}
			} else if recovered != nil || out.Code != 503 || strings.Contains(out.Body.String(), "private") {
				t.Fatal(recovered, out.Code, out.Body.String())
			}
			if mode != "nil" && mode != "typed-nil" && reader.closes != 1 {
				t.Fatal("close ownership", reader.closes)
			}
			if reader.maxRead > 64<<10 {
				t.Fatal("unbounded buffer", reader.maxRead)
			}
		})
	}
}

func TestDownloadParserBoundariesAndDates(t *testing.T) {
	for _, tc := range []struct {
		value               string
		size, start, length int64
		status              int
	}{
		{"", 0, 0, 0, 200}, {"bytes=0-0", 0, 0, 0, 416}, {"bytes=-0", 10, 0, 10, 416},
		{"bytes=-99", 10, 0, 10, 206}, {"bytes=0-9223372036854775807", 10, 0, 10, 206},
		{"bytes=9223372036854775806-", math.MaxInt64, math.MaxInt64 - 1, 1, 206},
		{"bytes=0-9223372036854775808", 10, 0, 10, 416}, {"bytes=+1-2", 10, 0, 10, 416},
		{"bytes=1--2", 10, 0, 10, 416}, {"bytes=1-2,999-", 10, 0, 10, 200}, {"other=1-2", 10, 0, 10, 200},
	} {
		a, n, s := downloadRange(tc.value, tc.size)
		if a != tc.start || n != tc.length || s != tc.status {
			t.Fatal(tc, a, n, s)
		}
	}
	for _, value := range []string{`"yes", bad`, `*, "yes"`, strings.Repeat(`"no",`, 64) + `"yes"`, `W/"yes", "unterminated`} {
		if _, valid := downloadETags([]string{value}, `"yes"`, true); valid {
			t.Fatal("invalid tag list", value)
		}
	}
	origin := time.Date(2026, 9, 8, 1, 2, 3, 500, time.UTC)
	for _, tc := range []struct {
		final           time.Time
		present, strong bool
	}{
		{origin.Add(-time.Hour), true, true}, {origin.Add(time.Hour), true, false}, {time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), true, false},
		{time.Date(1500, 1, 1, 0, 0, 0, 0, time.UTC), false, false}, {time.Time{}, false, false},
	} {
		date, strong := downloadModified(tc.final, origin)
		if !date.IsZero() != tc.present || strong != tc.strong || date.After(origin) {
			t.Fatal(tc, date, strong)
		}
		if tc.present {
			if _, err := http.ParseTime(date.Format(http.TimeFormat)); err != nil {
				t.Fatal(err)
			}
		}
		if !strong && downloadIfRange(date.Format(http.TimeFormat), `"tag"`, date, strong) {
			t.Fatal("weak date accepted")
		}
	}
}
