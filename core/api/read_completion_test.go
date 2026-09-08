package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	ghttp "github.com/Newton-School/gogo/core/http"
)

type readCompletionWriter struct {
	header                  http.Header
	status, headers, writes int
	count                   string
	err                     error
	writePanic              any
	attempted               []byte
	flushes                 int
}

func (w *readCompletionWriter) Header() http.Header { return w.header }
func (w *readCompletionWriter) WriteHeader(status int) {
	w.headers++
	w.status = status
}
func (w *readCompletionWriter) Write(body []byte) (int, error) {
	w.writes++
	w.attempted = append(w.attempted, body...)
	if w.writePanic != nil {
		panic(w.writePanic)
	}
	n := len(body)
	switch w.count {
	case "zero":
		n = 0
	case "short":
		n--
	case "negative":
		n = -1
	case "overcount":
		n++
	}
	return n, w.err
}
func (w *readCompletionWriter) Flush() { w.flushes++ }

type resourceReadCompletionCase struct {
	name, method string
	read         func(*http.Request) (any, error)
	status       int
	body         string
}

func resourceReadCompletionCases() []resourceReadCompletionCase {
	return []resourceReadCompletionCase{
		{"list", "GET", func(*http.Request) (any, error) { return Collection{Results: []Values{{"id": 1}}}, nil }, 200, `{"results":[{"id":1}]}`},
		{"detail", "GET", func(*http.Request) (any, error) { return Values{"id": 1}, nil }, 200, `{"id":1}`},
		{"tagged_detail", "GET", func(*http.Request) (any, error) { return taggedRepresentation{body: Values{"id": 1}}, nil }, 200, `{"id":1}`},
		{"method", "POST", nil, 405, `{"code":"METHOD_NOT_ALLOWED","detail":"Method not allowed"}`},
		{"unauthenticated", "GET", func(*http.Request) (any, error) { return nil, auth.ErrUnauthenticated }, 401, `{"code":"UNAUTHENTICATED","detail":"Authentication required"}`},
		{"denied", "GET", func(*http.Request) (any, error) { return nil, auth.ErrPermissionDenied }, 403, `{"code":"PERMISSION_DENIED","detail":"Permission denied"}`},
		{"not_found", "GET", func(*http.Request) (any, error) { return nil, ghttp.ErrNotFound }, 404, `{"code":"NOT_FOUND","detail":"Not found"}`},
		{"provider", "GET", func(*http.Request) (any, error) { return nil, errors.New("private provider detail") }, 500, `{"code":"INTERNAL_ERROR","detail":"Internal server error"}`},
		{"unavailable", "GET", func(*http.Request) (any, error) { return nil, ghttp.ErrUnavailable }, 503, `{"code":"UNAVAILABLE","detail":"Service unavailable"}`},
		{"encoding", "GET", func(*http.Request) (any, error) { return make(chan int), nil }, 500, `{"code":"INTERNAL_ERROR","detail":"Internal server error"}`},
		{"panic", "GET", func(*http.Request) (any, error) { panic("private callback detail") }, 503, `{"code":"UNAVAILABLE","detail":"Service unavailable"}`},
	}
}

func TestResourceReadCompletionRejectsFailedTransfers(t *testing.T) {
	for _, test := range resourceReadCompletionCases() {
		for _, mode := range []struct {
			name, count string
			err         error
		}{
			{"short", "short", nil}, {"zero", "zero", nil}, {"negative", "negative", nil}, {"overcount", "overcount", nil},
			{"partial_error", "short", errors.New("private transfer detail")},
			{"complete_error", "complete", errors.New("private transfer detail")},
		} {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				reads := 0
				handler := (&Resource{}).readHandler(func(r *http.Request) (any, error) {
					reads++
					if test.read == nil {
						t.Fatal("unsupported method reached read callback")
					}
					return test.read(r)
				})
				writer := &readCompletionWriter{header: http.Header{}, count: mode.count, err: mode.err}
				defer func() {
					if got := recover(); got != http.ErrAbortHandler {
						t.Errorf("failed transfer returned normally or exposed error: %v", got)
					}
					if writer.headers != 1 || writer.writes != 1 || writer.status != test.status || string(writer.attempted) != test.body {
						t.Errorf("response retried/appended or changed before failure: status=%d headers=%d writes=%d body=%q", writer.status, writer.headers, writer.writes, writer.attempted)
					}
					if test.method == "POST" && reads != 0 || test.method != "POST" && reads != 1 {
						t.Errorf("read operation retried or dispatched incorrectly: %d", reads)
					}
					assertResourceReadCompletionHeaders(t, writer.header, test.method)
				}()
				handler.ServeHTTP(writer, httptest.NewRequest(test.method, "/items/", nil))
			})
		}
	}
}

func TestResourceReadCompletionPreservesSuccessfulAndErrorEnvelopes(t *testing.T) {
	for _, test := range resourceReadCompletionCases() {
		t.Run(test.name, func(t *testing.T) {
			handler := (&Resource{}).readHandler(func(r *http.Request) (any, error) {
				if test.read == nil {
					t.Fatal("unsupported method reached read callback")
				}
				return test.read(r)
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, "/items/", nil))
			if response.Code != test.status || response.Body.String() != test.body {
				t.Fatal("public response mapping changed", response.Code, response.Body.String())
			}
			assertResourceReadCompletionHeaders(t, response.Header(), test.method)
			if test.name == "tagged_detail" && response.Header().Get("ETag") != bodyTag([]byte(test.body)) {
				t.Fatal("detail validator changed")
			}
		})
	}
}

func TestResourceReadCompletionHeadAndNotModifiedNeverWriteBody(t *testing.T) {
	for _, test := range []struct {
		name, method string
		condition    bool
		failure      error
		status       int
	}{
		{"head", "HEAD", false, nil, 200},
		{"conditional_get", "GET", true, nil, 304},
		{"conditional_head", "HEAD", true, nil, 304},
		{"head_denied", "HEAD", false, auth.ErrPermissionDenied, 403},
		{"head_not_found", "HEAD", false, ghttp.ErrNotFound, 404},
		{"head_provider", "HEAD", false, errors.New("private"), 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			handler := (&Resource{}).readHandler(func(*http.Request) (any, error) {
				reads++
				return taggedRepresentation{body: Values{"id": 1}}, test.failure
			})
			writer := &readCompletionWriter{header: http.Header{}, err: errors.New("unexpected body write")}
			request := httptest.NewRequest(test.method, "/items/1/", nil)
			if test.condition {
				request.Header.Set("If-None-Match", bodyTag([]byte(`{"id":1}`)))
			}
			handler.ServeHTTP(writer, request)
			if reads != 1 || writer.headers != 1 || writer.status != test.status || writer.writes != 0 || len(writer.attempted) != 0 {
				t.Fatal("HEAD/304 changed read semantics or attempted transfer", reads, writer)
			}
			assertResourceReadCompletionHeaders(t, writer.header, test.method)
		})
	}
}

func TestResourceReadCompletionPublicListAndDetailErrorsAbort(t *testing.T) {
	var unavailable *Resource
	for _, handler := range []http.Handler{unavailable.ListHandler(), unavailable.DetailHandler(nil)} {
		func() {
			writer := &readCompletionWriter{header: http.Header{}, count: "short"}
			defer func() {
				if got := recover(); got != http.ErrAbortHandler || writer.status != 503 || writer.headers != 1 || writer.writes != 1 {
					t.Errorf("public resource read discarded transfer failure: panic=%v writer=%+v", got, writer)
				}
			}()
			handler.ServeHTTP(writer, httptest.NewRequest("GET", "/items/", nil))
		}()
	}
}

func TestResourceReadCompletionPreservesWriterPanic(t *testing.T) {
	failure := errors.New("underlying writer panic")
	writer := &readCompletionWriter{header: http.Header{}, writePanic: failure}
	handler := (&Resource{}).readHandler(func(*http.Request) (any, error) { return Values{"id": 1}, nil })
	defer func() {
		if got := recover(); got != failure || writer.writes != 1 || writer.headers != 1 {
			t.Errorf("underlying writer panic changed or response retried: %v", got)
		}
	}()
	handler.ServeHTTP(writer, httptest.NewRequest("GET", "/items/", nil))
}

func TestResourceReadCompletionWriterPreservesCompleteCountAndUnwrap(t *testing.T) {
	underlying := &readCompletionWriter{header: http.Header{}}
	writer := resourceReadWriter{ResponseWriter: underlying}
	if writer.Unwrap() != underlying || writer.Header() == nil {
		t.Fatal("underlying writer was not retained")
	}
	for _, body := range [][]byte{[]byte("complete"), nil} {
		n, err := writer.Write(body)
		if n != len(body) || err != nil {
			t.Fatal("complete write return changed", n, err)
		}
	}
	if underlying.writes != 2 || string(underlying.attempted) != "complete" {
		t.Fatal("complete writes were repeated or changed")
	}
	if err := http.NewResponseController(writer).Flush(); err != nil || underlying.flushes != 1 {
		t.Fatal("ResponseController could not reach underlying capability", err, underlying.flushes)
	}
}

func assertResourceReadCompletionHeaders(t *testing.T, headers http.Header, method string) {
	t.Helper()
	if headers.Get("Cache-Control") != "private, no-store" || headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Content-Type") != "application/json" || headers.Get("Vary") != "Accept, Authorization, Cookie" {
		t.Error("resource security/content headers changed", headers)
	}
	if method == "POST" && headers.Get("Allow") != "GET, HEAD" {
		t.Error("unsupported method lost Allow header")
	}
	for _, values := range headers {
		for _, value := range values {
			if strings.Contains(value, "private transfer") {
				t.Error("writer diagnostic leaked to response")
			}
		}
	}
}
